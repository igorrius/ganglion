# P1 · M1 State cells — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans
> to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** state cells — switch, sensor and request — with desired and
reported state, version and history in SQLite, a `virtual` backend with
fault injection, observers, and conformance suites for stores and backends.

**Architecture:** package `cells` holds the model, the `Store` and `Backend`
interfaces and the `Service` that enforces kind rules, calls backends and
wakes observers. Stores: `cells.MemStore` and `cells/sqlstore`; both pass
`cells/storetest`. Backends: `cells/virtual`; it passes `cells/cellstest`.
SQLite is opened and migrated by `sqlitedb`, which M2 reuses for the queue.
The store is the source of truth: observers read changes from it by a global
sequence number, so a slow observer never loses a change.

**Tech stack:** Go 1.26, `database/sql`, `modernc.org/sqlite` v1.60.1 (pure
Go, ADR-0006).

**Spec:** [ADR-0014](../adr/0014-state-cells.md), [phase plan M1](p1-all-in-one.md#m1--state-cells-adr-0014--done),
spec principle 8, R-POL-5, spec 8.2 (manual override).

## Global constraints

- Pure Go, no CGO; the process stays small (principle 8).
- Every timestamp comes from an injected clock (`func() time.Time`) so tests
  control time; stored as Unix milliseconds UTC.
- Cell IDs match `^[a-z][a-z0-9_]*(\.[a-z0-9_]+)*$` (e.g. `load.big_boiler`);
  request IDs are generated as `request.<16 hex chars>`.
- Switch values are the JSON strings `"on"` and `"off"`; sensor values are
  any JSON; request status is one of `pending`, `approved`, `denied`,
  `expired`.
- Migrations live next to the package that owns the tables
  (`<pkg>/migrations/NNN_name.sql`) and are applied per component.
- Docs and comments in English.

## Review focus

1. **A slow or restarted observer** must still see every change, in order —
   `Watch` replays from a sequence number (task 4).
2. **Two writers set desired at once** — optimistic versioning rejects the
   stale one with `ErrConflict` (tasks 2, 4).
3. **A request answered after its `ttl`** stays `expired`; an answer to an
   already decided request is rejected (task 5).
4. **A late report from the backend for an older desired value** must not be
   taken for a manual change: reports carry a cause, and only `external`
   reports count as manual (task 6).
5. **The process dies after desired is stored but before the backend acts** —
   `Reconcile` re-applies every switch whose reported differs from desired
   (task 6).

---

### Task 1: `sqlitedb` — open SQLite and apply migrations (decision D-1)

**Files:**
- Create: `sqlitedb/sqlitedb.go`, `sqlitedb/sqlitedb_test.go`
- Create: `docs/adr/0016-sqlite-migrations.md`; add it to `docs/adr/README.md`

**Interfaces:**
- Produces: `func Open(ctx context.Context, path string) (*sql.DB, error)` —
  pragmas `journal_mode=WAL`, `busy_timeout=5000`, `foreign_keys=ON`,
  `synchronous=NORMAL`; `path == ":memory:"` gives a private in-memory DB
  (single connection).
- Produces: `func Migrate(ctx context.Context, db *sql.DB, component string, migrations fs.FS) error`
  — applies `NNN_*.sql` files in numeric order, each in its own transaction,
  recording `(component, version)` in `schema_migrations`.

- [x] **Step 1: Write failing tests** — `TestOpenSetsWAL` (file DB reports
  `wal`), `TestMigrateAppliesInOrderOnce` (two files create a table then add
  a column; a second `Migrate` is a no-op), `TestMigrateComponentsAreIndependent`
  (`a` and `b` both have `001_*.sql`), `TestMigrateFailedFileRollsBack`
  (a broken `002` leaves version 1 recorded and no partial schema),
  `TestMigrateRejectsBadNames` (`x.sql`, `1_a.txt`).
- [x] **Step 2: Run** `go test ./sqlitedb/` — FAIL (undefined).
- [x] **Step 3: Implement** `Open` and `Migrate`; write ADR-0016 (own
  migrator vs goose: a few dozen lines, no dependency, per-component).
- [x] **Step 4: Run** `go test -race ./sqlitedb/` — PASS.
- [x] **Step 5: Commit** `sqlitedb: open with WAL and per-component migrations`.

### Task 2: cell model, `Store` interface, `MemStore`, store conformance suite

**Files:**
- Create: `cells/cells.go` (model, errors, `Store`), `cells/memstore.go`,
  `cells/storetest/storetest.go`, `cells/memstore_test.go`

**Interfaces:**
- Produces:
  ```go
  type Kind string // KindSwitch "switch", KindSensor "sensor", KindRequest "request"
  type Cause string // CauseCommanded "commanded", CauseExternal "external"
  type Cell struct {
      ID            string
      Kind          Kind
      Backend       string          // "" for none
      Desired       json.RawMessage // nil when unset
      Reported      json.RawMessage
      ReportedCause Cause
      Version       int64           // 1 on create, +1 on every change
      Updated       time.Time
      Expires       time.Time       // requests only; zero otherwise
  }
  type Change struct {
      Seq     int64     // global, assigned by the store, strictly increasing
      CellID  string
      Version int64     // cell version after the change
      Field   string    // "created", "desired", "reported", "error"
      Value   json.RawMessage
      Cause   Cause     // for "reported"
      Source  string    // who made it: "flow:load-shedding", "agent", "backend:virtual", "owner"
      At      time.Time
  }
  var ErrNotFound, ErrExists, ErrConflict error
  type Store interface {
      Create(ctx context.Context, c Cell, src string) (Change, error)          // ErrExists
      Get(ctx context.Context, id string) (Cell, error)                        // ErrNotFound
      List(ctx context.Context) ([]Cell, error)                                // sorted by ID
      Update(ctx context.Context, c Cell, prevVersion int64, ch Change) (Change, error) // ErrConflict if version moved; stores c with Version = prevVersion+1 and appends ch with a new Seq, atomically
      ChangesSince(ctx context.Context, seq int64, limit int) ([]Change, error) // Seq > seq, ascending
      History(ctx context.Context, id string, limit int) ([]Change, error)      // newest first
      PendingRequestsBefore(ctx context.Context, t time.Time) ([]Cell, error)   // kind request, status pending, Expires <= t
  }
  func NewMemStore() *MemStore
  func storetest.Run(t *testing.T, newStore func(t *testing.T) cells.Store)
  ```
- [x] **Step 1: Write the suite** `storetest.Run` with subtests: create and
  get; create twice → `ErrExists`; get missing → `ErrNotFound`; update bumps
  the version and returns a change with a larger `Seq`; update with a stale
  `prevVersion` → `ErrConflict` and nothing stored; `ChangesSince` returns
  changes in order across cells and respects `limit`; `History` newest first;
  `PendingRequestsBefore` returns only pending requests due by `t`; `List`
  sorted. `memstore_test.go` calls `storetest.Run` with `NewMemStore`.
- [x] **Step 2: Run** `go test ./cells/...` — FAIL.
- [x] **Step 3: Implement** the model and `MemStore` (mutex, map, slice of
  changes). Request status is read from `Reported` (`{"status":"pending",…}`).
- [x] **Step 4: Run** `go test -race ./cells/...` — PASS.
- [x] **Step 5: Commit** `cells: model, Store interface, in-memory store and store suite`.

### Task 3: SQLite store

**Files:**
- Create: `cells/sqlstore/sqlstore.go`, `cells/sqlstore/migrations/001_cells.sql`,
  `cells/sqlstore/sqlstore_test.go`

**Interfaces:**
- Consumes: `sqlitedb.Open`, `sqlitedb.Migrate`, `cells.Store`, `storetest.Run`.
- Produces: `func New(ctx context.Context, db *sql.DB) (*Store, error)` —
  migrates component `cells`; tables `cells(id PK, kind, backend, desired,
  reported, reported_cause, version, updated_ms, expires_ms)` and
  `cell_changes(seq INTEGER PRIMARY KEY AUTOINCREMENT, cell_id, version, field,
  value, cause, source, at_ms)`, index on `(cell_id, seq)`.
- [x] **Step 1: Write failing tests** — `storetest.Run` on a temp-file DB;
  `TestReopenKeepsState` (close, reopen, same cell and history).
- [x] **Step 2: Run** `go test ./cells/sqlstore/` — FAIL.
- [x] **Step 3: Implement**; `Update` uses one transaction:
  `UPDATE … WHERE id=? AND version=?` (0 rows → `ErrConflict`) then
  `INSERT INTO cell_changes`.
- [x] **Step 4: Run** `go test -race ./cells/sqlstore/` — PASS.
- [x] **Step 5: Commit** `cells/sqlstore: SQLite store passing the store suite`.

### Task 4: `Service` — switches, sensors, history, watch

**Files:**
- Create: `cells/service.go`, `cells/service_test.go`

**Interfaces:**
- Consumes: `cells.Store`.
- Produces:
  ```go
  type Def struct { ID string; Kind Kind; Backend string }
  func NewService(store Store, opts ...Option) *Service   // WithClock(func() time.Time), WithBackend(name string, b Backend)
  func (s *Service) Define(ctx context.Context, d Def) error                      // idempotent; ErrConflict if kind or backend differs
  func (s *Service) Get(ctx context.Context, id string) (Cell, error)
  func (s *Service) SetDesired(ctx context.Context, id string, v json.RawMessage, src string, expectVersion int64) (Cell, error) // switch only; expectVersion 0 = any
  func (s *Service) Report(ctx context.Context, id string, v json.RawMessage, cause Cause, src string) (Cell, error)              // switch and sensor
  func (s *Service) History(ctx context.Context, id string, limit int) ([]Change, error)
  func (s *Service) Watch(ctx context.Context, fromSeq int64) <-chan Change      // replays from fromSeq, then live; closed when ctx ends
  func (c Cell) InSync() bool                                                     // desired equals reported (JSON-equal)
  func (c Cell) Manual() bool                                                     // !InSync() && ReportedCause == CauseExternal
  var ErrWrongKind, ErrInvalidValue, ErrInvalidID error
  ```
- [x] **Step 1: Write failing tests** — `TestDefineIsIdempotent`;
  `TestDefineRejectsBadID`; `TestSwitchAcceptsOnlyOnOff` (`"on"`, `"off"` ok,
  `true` → `ErrInvalidValue`); `TestSensorRejectsDesired` (`ErrWrongKind`);
  `TestSetDesiredWithStaleVersionConflicts`; `TestReportedCauseAndManual`
  (commanded report of the desired value → in sync; external report of a
  different value → `Manual()`); `TestWatchReplaysThenStreams` (three changes
  made before `Watch(ctx, 0)` arrive first, a fourth arrives live, in `Seq`
  order); `TestWatchFromSeqSkipsOlder`; `TestSlowWatcherLosesNothing`
  (200 changes while the reader sleeps; all arrive in order).
- [x] **Step 2: Run** `go test ./cells/` — FAIL.
- [x] **Step 3: Implement**. `Watch`: one goroutine per watcher reads
  `ChangesSince` in pages of 100 and waits on a notification channel the
  service signals after each successful write (store is the source of truth;
  the signal is only a wake-up).
- [x] **Step 4: Run** `go test -race ./cells/` — PASS.
- [x] **Step 5: Commit** `cells: service with kind rules, versioning and lossless watch`.

### Task 5: requests — ask, answer, expire

**Files:**
- Modify: `cells/service.go`; Create: `cells/request.go`, `cells/request_test.go`

**Interfaces:**
- Produces:
  ```go
  type Request struct { Question string `json:"question"`; Payload json.RawMessage `json:"payload,omitempty"` } // stored as Desired
  type Decision struct { Status string `json:"status"`; By string `json:"by,omitempty"` }              // stored as Reported
  func (s *Service) Ask(ctx context.Context, r Request, ttl time.Duration, src string) (Cell, error)  // new cell "request.<hex>", status pending
  func (s *Service) Answer(ctx context.Context, id string, approve bool, by string) (Cell, error)     // ErrDecided if not pending; expired if past Expires
  func (s *Service) ExpireDue(ctx context.Context) (int, error)                                       // marks due pending requests expired
  func DecisionOf(c Cell) (Decision, error)
  var ErrDecided error
  ```
- [x] **Step 1: Write failing tests** — `TestAskCreatesPendingRequest`
  (unique ID, `Expires` = now + ttl); `TestAnswerApproves`;
  `TestAnswerTwiceIsRejected` (`ErrDecided`); `TestAnswerAfterTTLExpires`
  (clock moved past `Expires`: result status `expired`, error `ErrDecided`);
  `TestExpireDueMarksOnlyDue` (two requests, one due).
- [x] **Step 2: Run** `go test ./cells/` — FAIL.
- [x] **Step 3: Implement** on top of `Store.Update` and
  `PendingRequestsBefore`.
- [x] **Step 4: Run** `go test -race ./cells/` — PASS.
- [x] **Step 5: Commit** `cells: confirmation requests with ttl`.

### Task 6: backends — `virtual` with fault injection, reconcile, backend suite

**Files:**
- Modify: `cells/service.go`; Create: `cells/backend.go`,
  `cells/virtual/virtual.go`, `cells/virtual/virtual_test.go`,
  `cells/cellstest/cellstest.go`, `cells/backend_test.go`

**Interfaces:**
- Produces:
  ```go
  type Reporter interface { Report(ctx context.Context, id string, v json.RawMessage, cause Cause, src string) (Cell, error) }
  type Backend interface { Apply(ctx context.Context, c Cell, r Reporter) error } // make reported follow desired; may report later
  func (s *Service) Reconcile(ctx context.Context) (int, error)  // calls Apply for every switch with a backend that is not in sync and not Manual()
  // virtual
  func virtual.New(opts ...virtual.Option) *virtual.Backend      // WithDelay(d), WithClock
  func (b *Backend) FailNext(n int, err error)                   // next n Apply calls fail
  func (b *Backend) Flip(ctx context.Context, r cells.Reporter, id string, v json.RawMessage) error // external change, as if by hand
  func cellstest.Run(t *testing.T, newBackend func(t *testing.T) (cells.Backend, Flipper))
  ```
  `SetDesired` on a switch with a backend calls `Apply` after the desired
  change is stored; an `Apply` error is stored as an `"error"` change (the
  cell stays out of sync) and returned.
- [x] **Step 1: Write failing tests** — suite `cellstest.Run`: apply makes
  the cell in sync with cause `commanded`; with a delay, in sync only after
  the delay; `FailNext` → error change recorded, cell out of sync, next apply
  succeeds; `Flip` → `Manual()` true. `TestLateCommandedReportIsNotManual`
  (desired on→off while a delayed report of "on" is in flight: not
  `Manual()`). `TestReconcileReappliesAfterCrash` (desired stored, backend
  not called; `Reconcile` brings it in sync; a `Manual()` cell is left alone).
- [x] **Step 2: Run** `go test ./cells/...` — FAIL.
- [x] **Step 3: Implement**.
- [x] **Step 4: Run** `go test -race ./cells/...` — PASS.
- [x] **Step 5: Commit** `cells: backends, virtual backend with fault injection, reconcile`.

### Task 7: memory check and docs

**Files:**
- Modify: `docs/architecture/05-code-map.md`, `docs/README.md`,
  `docs/plans/p1-all-in-one.md` (tick M1), `go.mod`

- [x] **Step 1:** Measure: a throwaway program (scratch, not committed)
  opens a file DB, defines 50 cells and makes 10,000 changes; record RSS
  peak and the size of a `ganglion`-like binary with SQLite linked; must stay
  well inside 128 MB.
- [x] **Step 2:** `make check` and `GOOS=wasip1` lint stay green.
- [x] **Step 3:** Update the code map (`sqlitedb`, `cells`, `cells/sqlstore`,
  `cells/virtual`, `cells/storetest`, `cells/cellstest`), mark M1 done in the
  phase plan with the measured numbers, update "Where we are".
- [x] **Step 4: Commit** `docs: M1 state cells done`.
