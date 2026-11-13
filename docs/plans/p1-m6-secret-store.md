# P1 · M6 task 6.5: the secret store — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** a secret is an encrypted row in the node's database, scoped to one module or to the
node, set with `ganglion secret set` and read at the next use with no restart; nobody creates an
environment variable for it (board card G-149).

**Architecture:**

- **One new package, `secrets/vault`:** the master key (a file the node makes, or one the
  operator supplies), the encrypted table, the locked state, the status line, the metrics.
- **Existing parts that change:**
  - `secrets`: `Scope`, `First` (several sources in order), the name rule.
  - `settings`: a secret field takes `store:NAME` beside `env:NAME`; `Sources` replaces the
    single source in `Resolve`.
  - `modhost`, `tool/builtin`: a module's secrets come from a source made for that module;
    removing a module forgets its scope.
  - `config`: the `Vault` provider, the per-module source, the master key among the node's own
    secrets and the sandbox's denied files.
  - `cmd`: `ganglion secret set|rm|list`; `run` reads `store:` references before the node
    starts; `mcp`'s `config.show` shows them as set or unset without the key.
  - `agent`: `/secrets`.
- No new Go module: AES-256-GCM, `crypto/rand` and `encoding/base64` are the standard library.

**Tech stack:** Go 1.27, SQLite (`modernc.org/sqlite`), `crypto/aes`, `crypto/cipher`.

**Spec:** 7.14 (R-CFG-2, R-CFG-9, R-CFG-10), 7.5 (`secret.get`, R-RUN-9), 7.8, 7.12 (R-CMD-3),
7.9 (R-OBS-7, R-OBS-8, R-OBS-11);
[ADR-0077](../adr/0077-encrypted-secret-store-on-the-nodes-key.md),
[ADR-0059](../adr/0059-node-config-file.md), [ADR-0016](../adr/0016-sqlite-migrations.md),
[ADR-0039](../adr/0039-command-sandbox.md), [ADR-0031](../adr/0031-agent-security-baseline.md).
Read them with this plan. The [design note](secrets-intake-design.md) is background; the request,
the page and the return channel in it are **not** part of this plan.

## Global Constraints

- All documentation, comments and doc comments are in English; the [conventions](../conventions.md)
  apply (constructors, no `nil` collaborators, named types for closed sets and for function
  fields, small functions, the linter's limits, an injected clock, no sleeping in tests, the
  stdlib `testing` package only).
- Work on branch `m6-secret-store`. Commit after each task with `make lint test` green; stage
  only the task's own files by path (`git add <path>…`, never `git add -A`). Push after each task.
- Architecture docs change in the same commit as the code (`05-code-map.md` rows and dependency
  rules; `03-components.md` when components connect differently).
- Only `config` builds and connects components; domain packages never import `samber/do`. Only
  `cmd/...` imports `urfave/cli`.
- **A secret's value never appears** in a log, an error, a metric label, a journal row, the
  history, a reply, `argv`, or a test's failure message. Errors and logs carry the scope and the
  name.
- **No tool and no command returns a value.** The agent gets no new tool in this plan.
- Every metric and log line of a task lands in that task's commit (conventions §7).
- Exact values: the key is 32 bytes; the cipher is AES-256-GCM with a 12-byte random nonce; a
  value is at most 4096 bytes; the key's variable is `GANGLION_MASTER_KEY` (standard base64 of
  the 32 bytes) or `GANGLION_MASTER_KEY_FILE`; the key's file is `master.key` beside the
  database, mode `0600`; scopes are `node` and `module:<full name>` (`module:local/deye-reader`);
  the migrator's component name is `secrets`.

## Rulings this plan makes (call; the owner may overturn them in review)

1. **The package is `secrets/vault`.** `secrets` keeps the interface and the environment source
   (conventions §1: interfaces in small packages); `settings` imports `secrets` and must not pull
   in SQLite.
2. **`ganglion secret` writes beside a running node** and does not take the database lock of
   ADR-0019. That lock keeps two cores off one file; SQLite's own locking makes a second writer
   to one table safe. The command migrates only the `secrets` component.
3. **`run` reads `store:` references before the container exists**, by opening the database for
   a moment, and only when the file has such a reference. The transports need the bot token as a
   plain value in `config.Bootstrap`, as today.
4. **`ganglion mcp` never reads the key.** `config.show` reports a `store:NAME` as set when its
   row exists.
5. **`/secrets reset` is confirmed by a second command**, `/secrets reset confirm`. No command
   has a confirmation button today, and a reset is rare enough not to add one.
6. **"The modules that name each" is the scope.** `/secrets` groups rows by scope; a module's
   scope is read by that module alone. It does not list which config field names a `node` row.
7. **`/status` gets a line only when the store is locked**, in the problems section.
8. **A test node gets a key made in memory** (`Bootstrap.MasterKey == ""`): nothing is written
   beside a `:memory:` database. The lab sets a path in its temporary folder.
9. **A name is `^[A-Za-z_][A-Za-z0-9_.-]{0,127}$`** in the store, which covers every name a
   manifest uses today; `store:NAME` in the config file keeps the stricter rule of `env:NAME`.
10. **Redaction needs no new mechanism.** A `store:` value read at start is in
    `Bootstrap.SecretValues`; a value handed to a module goes through the module host's `given`
    set (`modhost/redact.go`), which already takes values that change while the module runs.
11. **The master key's variable is one of the node's own secrets**, so no module reads it
    through the environment fallback and no skill script lists it.

## Review Focus

1. **Scope isolation:** a module reads its own scope and the environment, never another
   module's scope, never `node`; a row moved to another scope or name in the database does not
   decrypt.
2. **The key is never replaced over existing rows:** a wrong or missing key locks the store;
   only `/secrets reset confirm` drops the rows.
3. **No value leaks:** errors, logs, `secret list`, `/secrets`, `config.show`, the history note.
4. **No restart:** a value set by the command while the node runs is what the module gets at
   its next `secret.get`, and it is hidden in that module's log lines.
5. **The sandbox:** a host command and a skill script can read neither the key file nor the
   key's variable.

## Files

| File | Change |
|---|---|
| `secrets/secrets.go`, `secrets/secrets_test.go` | `Scope`, `ModuleScope`, `First`, `ValidName` |
| `secrets/vault/key.go`, `key_test.go` | New: `Key`, `LoadKey`, `KeyPath` |
| `secrets/vault/vault.go`, `cipher.go`, `vault_test.go`, `migrations/001_secrets.sql` | New: the store |
| `secrets/vault/lock.go`, `lock_test.go`, `status.go`, `telemetry.go` | New: the locked state, reset, `/status`, metrics |
| `settings/types.go`, `settings/settings.go`, `settings/models.go`, their tests | `store:NAME`, `Sources`, `StoreNames` |
| `schemas/node.schema.json`, `ganglion.example.yaml` | `secretRef` takes `store:NAME` |
| `cmd/run.go`, `cmd/mcp.go`, `cmd/secret.go`, `cmd/root.go`, their tests | Reading `store:` references; the `secret` commands |
| `config/bootstrap.go`, `config/storage.go`, `config/modhost.go`, `config/agent.go`, `config/sandbox.go`, `config/status.go`, `config/config.go`, `config/lab.go` | `MasterKey`, the `Vault` provider, wiring |
| `modhost/host.go`, `modhost/redact.go`, `modhost/load.go`, `modhost/admin.go`, tests | `SecretsFor`, `ForgetSecrets` |
| `tool/builtin/module.go`, tests | A module's source for the install check and `module.status` |
| `sandbox/deny.go`, `sandbox/deny_test.go` | `NodeFiles.MasterKey` |
| `agent/secrets_cmd.go`, `agent/commands.go`, `agent/agent.go`, tests | `/secrets` |
| `texts/texts.go`, `texts/lang/{en,ru,uk}.yaml` | New keys |
| `internal/chatlab/node.go`, `tools/chatlab/scenarios/secrets.yaml` | The lab's key; a scenario |
| `docs/…` | Code map, components, conventions §8, `deploy-railway.md`, README, the phase plan |

---

### Task 1: `secrets` — scopes and sources in order

**Files:**
- Modify: `secrets/secrets.go`, `secrets/secrets_test.go`

**Interfaces:**
- Produces:

```go
// secrets

// Scope is who a stored secret belongs to (ADR-0077).
type Scope string

// ScopeNode holds the node's own secrets, named by store:NAME in the file.
const ScopeNode Scope = "node"

// ModuleScope is the scope of the module with this full name
// ("local/deye-reader" → "module:local/deye-reader").
func ModuleScope(module string) Scope

// ValidName reports whether name can name a stored secret:
// ^[A-Za-z_][A-Za-z0-9_.-]{0,127}$.
func ValidName(name string) bool

// First reads each source in order and returns the first value that is
// not empty. An error of a source ends the search and is returned.
type First []Source

func (f First) Secret(name string) (string, error)
```

- [x] **Step 1: Failing tests** (table tests, `package secrets`):
  - `ModuleScope("local/x")` is `"module:local/x"`;
  - `ValidName`: `DEYE_APP_ID`, `wifi-home`, `a.b` pass; `""`, `1A`, a name with a space, with
    `/`, with `:`, and one of 129 characters fail;
  - `First`: the second source answers when the first has `""`; the first wins when both have a
    value; an error of the first is returned and the second is not asked (a source that fails
    the test when called); an empty `First` returns `""` and no error.
- [x] **Step 2: Run, see them fail:** `go test ./secrets -run 'Scope|ValidName|First'`.
- [x] **Step 3: Implement.** Update the package comment: the encrypted store is in
  `secrets/vault`.
- [x] **Step 4: `make lint test`; commit, push.**

```bash
git commit -m "secrets: scopes and sources read in order (ADR-0077)"
```

---

### Task 2: `secrets/vault` — the master key (R-CFG-10)

**Files:**
- Create: `secrets/vault/key.go`, `secrets/vault/key_test.go`

**Interfaces:**
- Consumes: `secrets.Source`.
- Produces:

```go
// Package vault is the node's encrypted secret store (ADR-0077).
package vault

const (
	KeyEnv  = "GANGLION_MASTER_KEY" // standard base64 of 32 bytes, or its _FILE
	KeyFile = "master.key"          // beside the database when KeyEnv is not set
	KeySize = 32
)

// Key is the store's master key.
type Key [KeySize]byte

// ErrKey is a key that cannot be used: not 32 bytes, not base64, a file
// that cannot be read or made. The error names the variable or the path,
// never the bytes.
var ErrKey = errors.New("vault: the master key cannot be used")

// KeyPath is master.key beside the database at db.
func KeyPath(db string) string

// NewKey is a random key (crypto/rand).
func NewKey() (Key, error)

// LoadKey returns the operator's key when src has KeyEnv or KeyEnv_FILE
// (base64, surrounding whitespace ignored). Otherwise it reads the 32 raw
// bytes of the file at path; when that file does not exist it makes one
// with a new key, mode 0600, created exclusively, and created is true. A
// file that appeared meanwhile is read instead. path "" with no operator
// key returns a new key that is written nowhere (a test node).
func LoadKey(src secrets.Source, path string) (key Key, created bool, err error)
```

- [x] **Step 1: Failing tests** (`t.TempDir()`, `secrets.Env` with fake `Getenv`/`ReadFile`):
  - no variable, no file: a file of exactly 32 bytes appears with mode `0600`, `created` is
    true; a second call returns the same key and `created` false;
  - the variable set to base64 of 32 bytes: that key, no file is made at `path`;
  - `GANGLION_MASTER_KEY_FILE`: the key from the file, trailing newline ignored;
  - errors wrap `ErrKey` and do not contain the key's text: base64 of 31 bytes, not base64, a
    key file of 31 bytes, a key file that is a directory, a parent folder that does not exist;
  - a file of mode `0644` is read (the node does not refuse the operator's file) — and a test
    that the file `LoadKey` makes is `0600` under a `umask` of 0;
  - `path == ""`: two calls give two different keys, nothing is written in the working folder;
  - two goroutines call `LoadKey` on one new path: both return the same key.
- [x] **Step 2: Run, see them fail:** `go test ./secrets/vault -run Key`.
- [x] **Step 3: Implement.** Create with `os.OpenFile(path, O_WRONLY|O_CREATE|O_EXCL, 0o600)`,
  write, `Sync`, close; on `os.ErrExist` read the file. Never truncate or rewrite an existing
  file.
- [x] **Step 4: `make lint test`; commit, push.**

```bash
git commit -m "vault: the master key — the operator's, or a file the node makes (R-CFG-10)"
```

---

### Task 3: `secrets/vault` — the encrypted table (R-CFG-9)

**Files:**
- Create: `secrets/vault/vault.go`, `secrets/vault/cipher.go`, `secrets/vault/vault_test.go`,
  `secrets/vault/migrations/001_secrets.sql`, `secrets/vault/telemetry.go`

**Interfaces:**
- Consumes: `Key` (task 2); `secrets.Scope`, `secrets.ValidName`, `secrets.Source` (task 1);
  `sqlitedb.Migrate`.
- Produces:

```go
// MaxValue is the longest value, in bytes.
const MaxValue = 4096

var (
	ErrName    = errors.New("vault: not a secret's name")
	ErrEmpty   = errors.New("vault: an empty value")
	ErrTooLong = errors.New("vault: a value over 4096 bytes")
	ErrLocked  = errors.New("vault: the store is locked: its key does not fit") // task 4
)

// Entry is a stored secret without its value.
type Entry struct {
	Scope secrets.Scope
	Name  string
	SetAt time.Time
	By    string // "cli", or the principal who set it
}

// Vault is the store. Safe for concurrent use and beside another process
// that writes the same table.
type Vault struct{ /* db, aead, now, instruments, locked */ }

// Open migrates the "secrets" component and returns the store on key.
func Open(ctx context.Context, db *sql.DB, key Key, now func() time.Time) (*Vault, error)

// Get is the value of name in scope; "" when it is not stored. A row that
// does not decrypt is an error naming the scope and the name.
func (v *Vault) Get(ctx context.Context, scope secrets.Scope, name string) (string, error)

// Set stores value, replacing an earlier one.
func (v *Vault) Set(ctx context.Context, scope secrets.Scope, name, value, by string) error

// Remove deletes one secret; false when there was none.
func (v *Vault) Remove(ctx context.Context, scope secrets.Scope, name string) (bool, error)

// RemoveScope deletes every secret of scope and returns how many.
func (v *Vault) RemoveScope(ctx context.Context, scope secrets.Scope) (int, error)

// List is every entry, by scope then name.
func (v *Vault) List(ctx context.Context) ([]Entry, error)

// Source reads scope as a secrets.Source, for the code that takes one.
func (v *Vault) Source(scope secrets.Scope) secrets.Source

// Has reports whether a row exists, without a key: for a read-only
// database (ganglion mcp). A database without the table has none.
func Has(ctx context.Context, db *sql.DB, scope secrets.Scope, name string) (bool, error)
```

The migration:

```sql
CREATE TABLE secrets (
    scope      TEXT    NOT NULL,
    name       TEXT    NOT NULL,
    nonce      BLOB    NOT NULL,
    ciphertext BLOB    NOT NULL,
    set_ms     INTEGER NOT NULL,
    set_by     TEXT    NOT NULL,
    PRIMARY KEY (scope, name)
) STRICT;

CREATE TABLE secret_meta (
    key        TEXT PRIMARY KEY,
    nonce      BLOB NOT NULL,
    ciphertext BLOB NOT NULL
) STRICT;
```

The cipher (`cipher.go`, pure): `seal(aead, scope, name, plaintext) (nonce, ciphertext)` and
`open(aead, scope, name, nonce, ciphertext) ([]byte, error)`. The additional data is
`"ganglion-secret-v1\x00" + scope + "\x00" + name`.

- [x] **Step 1: Failing tests** (`sqlitedb.Open(ctx, ":memory:")`, a fixed clock):
  - `Set` then `Get` returns the value; `Get` of an unknown name is `""`, no error;
  - `Set` twice keeps one row with the second value and the second time;
  - two scopes hold the same name with different values;
  - `Set` refuses: a name that fails `ValidName` (`ErrName`), `""` (`ErrEmpty`), 4097 bytes
    (`ErrTooLong`); 4096 bytes pass; none of the errors contains the value;
  - the stored `ciphertext` and `nonce` columns do not contain the value's bytes; two `Set`s of
    the same value give different nonces and ciphertexts;
  - **abuse:** `UPDATE secrets SET scope = 'module:local/b'` on a row of `module:local/a`, then
    `Get` in `module:local/b` is an error that names scope and name, never a value; the same
    for a changed `name`; the same for one flipped byte of `ciphertext`;
  - **abuse:** the database reopened with another key: task 4 covers it; here `open` with
    another key fails;
  - `Remove` returns true once and false after; `RemoveScope` removes only its scope and
    returns the count;
  - `List` is ordered by scope then name, with times from the clock and `By`; no field of
    `Entry` is a value;
  - `Source(scope).Secret(name)` equals `Get`;
  - `Has` on a database that never had the migration is false with no error; true after `Set`;
    it works on a database opened with `sqlitedb.OpenReadOnly` (a file in `t.TempDir()`);
  - metrics: after one `Set`, one `Get` of a stored name and one of an unknown name, the
    Prometheus text has the counter with `op`/`outcome` pairs `set/ok`, `get/ok`, `get/unset`
    (read it back as `modstore`'s telemetry test does).
- [x] **Step 2: Run, see them fail:** `go test ./secrets/vault`.
- [x] **Step 3: Implement.**
  - `Open`: `fs.Sub(migrations, "migrations")`, `sqlitedb.Migrate(ctx, db, "secrets", sub)`,
    `aes.NewCipher(key[:])`, `cipher.NewGCM`. The check value is task 4: leave `secret_meta`
    unused here.
  - `Set` is an upsert (`ON CONFLICT (scope, name) DO UPDATE`).
  - `Source` uses `context.Background()`: `secrets.Source` has no context, and a read of one
    row needs no deadline beyond SQLite's busy timeout. Say so in its doc comment.
  - `telemetry.go`: one counter of operations with labels `op` (a named type: `get`, `set`,
    `remove`, `reset`) and `outcome` (`ok`, `unset`, `locked`, `error`); name it and register
    it the way `modstore/telemetry.go` does. No scope and no name in a label.
  - Errors: `fmt.Errorf("vault: get %s %s: %w", scope, name, err)`.
- [x] **Step 4: `make lint test`; commit, push.**

```bash
git commit -m "vault: secrets as encrypted rows, bound to their scope and name (R-CFG-9)"
```

---

### Task 4: `secrets/vault` — a key that does not fit locks the store (R-CFG-10)

**Files:**
- Create: `secrets/vault/lock.go`, `secrets/vault/lock_test.go`, `secrets/vault/status.go`
- Modify: `secrets/vault/vault.go`, `texts/texts.go`, `texts/lang/{en,ru,uk}.yaml`

**Interfaces:**
- Consumes: task 3.
- Produces:

```go
// Locked reports whether the key did not fit the store at Open, or at the
// last Reset. While locked: Get returns "" and no error, Set returns
// ErrLocked, Remove and RemoveScope still delete, List still lists.
func (v *Vault) Locked() bool

// Reset deletes every secret, writes the check value for the key the
// store was opened with, and unlocks it. It returns how many it deleted.
func (v *Vault) Reset(ctx context.Context) (int, error)

// Name and Status implement status.Source: one failing line in the
// problems section while the store is locked, nothing otherwise.
func (v *Vault) Name() string
func (v *Vault) Status(ctx context.Context, s status.Scope) ([]status.Check, error)
```

```go
// texts
StatusSecretsLocked       Key = "status.secrets_locked"
StatusReasonSecretsKey    Key = "status.reason_secrets_key"
```

English: `status.secrets_locked: "Secret store: locked"`,
`status.reason_secrets_key: "its key does not fit the stored secrets; /secrets reset drops them"`.
Write the Russian and Ukrainian lines in the same commit.

- [x] **Step 1: Failing tests:**
  - the first `Open` on an empty database writes one `secret_meta` row with key `check`; a
    second `Open` with the same key is not locked and reads the secrets;
  - `Open` with another key on a database that has the check row: no error, `Locked()` is
    true; `Get` of a stored name is `""`; `Set` is `ErrLocked`; `List` still returns the
    entries; `Remove` works; **the check row and the secrets rows are unchanged** (compare the
    blobs before and after);
  - `Reset` on a locked store returns the count, leaves no secret, and `Locked()` is false;
    `Set` then `Get` work; reopening with the *old* key is now locked;
  - `Reset` on an unlocked store also drops everything and stays unlocked;
  - two `Open`s on one new file database from two goroutines with the same key: neither is
    locked (the check row is inserted with `ON CONFLICT DO NOTHING`, then read and verified);
  - `Status`: no line when unlocked; one `status.Fail` line in `status.SectionProblems` with
    `texts.StatusSecretsLocked` and `texts.StatusReasonSecretsKey` when locked;
  - the counter has `get/locked` after a `Get` on a locked store.
- [x] **Step 2: Run, see them fail:** `go test ./secrets/vault -run 'Lock|Reset|Status'`.
- [x] **Step 3: Implement.** The check value is `seal` of the constant
  `"ganglion-secret-store"` under scope `""` and name `"check"`. `Reset` runs in
  `sqlitedb.Immediate`: delete from `secrets`, replace the check row. `locked` is an
  `atomic.Bool`. `Open` logs nothing: `config` passes the logger's concern to `/status`, and
  `cmd` prints the locked state itself (tasks 6 and 7).
- [x] **Step 4: `make lint test`; commit, push.**

```bash
git commit -m "vault: a key that does not fit locks the store; reset drops it (R-CFG-10)"
```

---

### Task 5: `settings` — `store:NAME` in a secret field (R-CFG-2)

**Files:**
- Modify: `settings/types.go`, `settings/settings.go`, `settings/models.go` and their tests,
  `schemas/node.schema.json`, `ganglion.example.yaml`, `cmd/run.go`, `cmd/mcp.go`,
  `tools/modelcompare/main.go`, `cmd/run_test.go`, `cmd/mcp_test.go`

**Interfaces:**
- Consumes: `secrets.Source`, `secrets.None`.
- Produces:

```go
// settings

// Scheme is where a secret reference reads from.
type Scheme string

const (
	SchemeEnv   Scheme = "env"   // the variable NAME or the file NAME_FILE
	SchemeStore Scheme = "store" // the node scope of the secret store
)

// Sources are where references are read, one per scheme.
type Sources struct{ Env, Store secrets.Source }

func (r SecretRef) Scheme() Scheme // "" for an empty reference
func (r SecretRef) Name() string   // without the scheme
func (r SecretRef) Resolve(s Sources) (string, error)

// SecretNames keeps its meaning: the environment variables the file
// names. A store: reference is not one.
func (f File) SecretNames() []string

// StoreNames are the names every store: reference of the file names.
func (f File) StoreNames() []string

func (m Models) Keys(s Sources) (map[string]string, error) // was (src secrets.Source)
```

- [x] **Step 1: Failing tests:**
  - `SecretRef` unmarshals `env:TELEGRAM_BOT_TOKEN` and `store:TELEGRAM_BOT_TOKEN`; it refuses
    `store:`, `store:lower`, `vault:X`, a literal, and the error says
    `must be env:NAME or store:NAME` and never repeats the value;
  - `Resolve`: an `env:` reference asks only `Sources.Env`, a `store:` one only `Sources.Store`
    (the other source fails the test when called); an empty reference is `""`;
  - `SecretNames` leaves `store:` references out; `StoreNames` lists them, providers' keys
    included (`Models.KeyNames` gets the same split: read it and keep one function per scheme);
  - the schema test (ADR-0023) still passes with `secretRef`'s pattern
    `^(env|store):[A-Z_][A-Z0-9_]*$` and its description updated;
  - `cmd`: `fromFile` with `telegram.token: store:BOT` and a `Sources.Store` that holds `BOT`
    fills `Bootstrap.BotToken`; `announce`'s "no token" warning names the reference whole
    (`store:BOT`), not a variable;
  - `config.show` (`cmd/mcp_test.go`): an `env:` field is unchanged; a `store:` field shows
    `store:NAME (unset)` here — task 6 makes it read the database.
- [x] **Step 2: Run, see them fail:** `go test ./settings ./cmd`.
- [x] **Step 3: Implement.** Every caller that passed a `secrets.Source` passes
  `settings.Sources{Env: src, Store: secrets.None{}}` for now: `cmd/run.go` (`bootstrap`,
  `fromFile`, `agentSettings`), `cmd/mcp.go` (`runSettings` and below),
  `tools/modelcompare/main.go`. In `secretSetting`, `diag.Setting.Env` is set only for
  `SchemeEnv`. Update `ganglion.example.yaml`'s header comment and `run`'s `Description`.
- [x] **Step 4: `make lint test`; commit, push.**

```bash
git commit -m "settings: a secret field takes store:NAME beside env:NAME (R-CFG-2)"
```

---

### Task 6: wiring — modules read their scope; the key is the node's own

**Files:**
- Modify: `config/bootstrap.go`, `config/storage.go`, `config/modhost.go`, `config/agent.go`,
  `config/sandbox.go`, `config/status.go`, `config/config.go` (register the provider),
  `config/lab.go`, `modhost/host.go`, `modhost/redact.go`, `modhost/load.go`,
  `modhost/admin.go`, `tool/builtin/module.go`, `sandbox/deny.go`, `cmd/run.go`, `cmd/mcp.go`,
  `internal/chatlab/node.go`, and the tests beside each
- Modify: `docs/architecture/03-components.md`, `docs/architecture/05-code-map.md`

**Interfaces:**
- Consumes: `vault.Open`, `vault.LoadKey`, `vault.KeyPath`, `vault.KeyEnv`, `vault.Has`,
  `(*Vault).Source`, `(*Vault).RemoveScope`, `secrets.First`, `secrets.ModuleScope`,
  `settings.Sources`, `File.StoreNames`.
- Produces:

```go
// modhost
// SecretsFor is the secret source of one module, by its full name.
type SecretsFor func(module string) secrets.Source

// ForgetSecrets drops what the node stores for a removed module.
type ForgetSecrets func(ctx context.Context, module string) error

type Deps struct {
	// ...
	Secrets SecretsFor    // was secrets.Source
	Forget  ForgetSecrets // called by Remove
	// ...
}
```

```go
// tool/builtin
// ModuleSecrets is the secret source of one module, by its full name.
// tool/builtin does not import modhost, so it declares its own type;
// config gives both the same function.
type ModuleSecrets func(module string) secrets.Source

type Modules struct {
	// ...
	Secrets ModuleSecrets // was secrets.Source
}
```

```go
// config
type Bootstrap struct {
	// ...
	// MasterKey is where the secret store's key file is: master.key beside
	// the database for a node the command line starts; "" makes a key in
	// memory (a test node).
	MasterKey string
}

// Vault provides the secret store on the node's key (ADR-0077).
func Vault() do.Provider[*vault.Vault]
```

```go
// sandbox
type NodeFiles struct {
	Binary, DB, Settings, LogDir, ModulesDir, ClaudeDir, Workspace, Languages, MasterKey string
}
```

- [x] **Step 1: Failing tests:**
  - `modhost` (the probe module of `runner/runnertest/probe`, as the existing host tests use
    it): a module whose manifest grants `PROBE_TOKEN` gets the value its own source returns; a
    second module with the same manifest and a different source gets its own value;
  - `modhost`: the source's value changes between two calls of the module; the second call
    gets the new value, and **a log line the module writes with the new value shows
    `[redacted]`** (the existing `given` test, extended);
  - `modhost.Remove`: `Forget` is called once with the full name, after the module stopped; a
    `Forget` error is returned joined with the others and the module is still removed;
  - `config` (a container on `:memory:` with fakes, as `config_test.go` builds one): `Vault` is
    provided; the module source for `local/a` returns the vault's value in
    `module:local/a`, falls back to `Bootstrap.Secrets` for a name the vault lacks, and
    **does not return a value stored in `module:local/b` or in `node`**;
  - `config`: `nodeSecrets(b)` contains `GANGLION_MASTER_KEY`, so
    `modstore.NodeSecret(nodeSecrets(b), "GANGLION_MASTER_KEY_FILE")` is true and a manifest
    that names either is refused at install (`tool/builtin` test: the refusal says the secret
    is the node's own);
  - `sandbox.DenyList`: `NodeFiles.MasterKey` is in the list when the database lies in the
    working folder (the case `dataFiles` does not cover with the whole folder);
  - `config/hostexec_accept_test.go` (the real sandbox; it skips where the sandbox is off):
    `cat <master key path>` from `host.exec` fails, and `env` does not print
    `GANGLION_MASTER_KEY`;
  - `tool/builtin`: the install check counts a secret as set when only the module's scope has
    it, and as unset when only *another* module's scope has it;
  - `cmd`: `bootstrap` with a file that has `telegram.token: store:BOT`, a database in
    `t.TempDir()` whose vault holds `node`/`BOT`: `Bootstrap.BotToken` is the value and
    `Bootstrap.MasterKey` is `master.key` beside the database; with **no** `store:` reference
    the database file is not created by `bootstrap` (stat it);
  - `cmd`: a `store:` reference on a locked store reads as unset, and `run` logs one warning
    that names the reference and says the store is locked;
  - `cmd/mcp_test.go`: `config.show` shows `store:BOT (set)` for a row that exists and
    `(unset)` otherwise, with no key file present.
- [x] **Step 2: Run, see them fail:** `go test ./modhost ./tool/builtin ./config ./sandbox ./cmd`.
- [x] **Step 3: Implement.**
  - `config.Vault`: `vault.LoadKey(moduleSecrets(b), b.MasterKey)`, then
    `vault.Open(Startup, Database.DB, key, time.Now)`. A new key file is worth one log line;
    `config` holds no log lines about the product, so `LoadKey`'s `created` goes to a small
    function in `vault` that logs it (`vault.Announce(logger, created, locked)`), called here.
  - The module source: `func(module string) secrets.Source { return secrets.First{v.Source(secrets.ModuleScope(module)), moduleSecrets(b)} }`.
    `Forget`: `v.RemoveScope(ctx, secrets.ModuleScope(module))`.
  - `modhost`: `newGiven` and `secret` call `h.d.Secrets(l.name)` with the module's **full**
    name; `Remove` calls `h.d.Forget(ctx, module)` after `Store.RemoveLocal` and joins its
    error.
  - `nodeSecrets` appends `vault.KeyEnv`.
  - `config.Sandbox` passes `MasterKey: b.MasterKey`. A key given as
    `GANGLION_MASTER_KEY_FILE` is already denied by `DenyList`'s `_FILE` rule.
  - `StatusSources` appends the vault.
  - `cmd/run.go`: `bootstrap` sets `b.MasterKey = vault.KeyPath(v.DB)`. A new function
    `storeSource(ctx, f, db, src) (secrets.Source, func(), error)` returns `secrets.None{}`
    when `len(f.StoreNames()) == 0`; otherwise it opens the database (`sqlitedb.Open`), loads
    the key, opens the vault and returns `v.Source(secrets.ScopeNode)` with a closer. Call it
    before `fromFile` and pass `settings.Sources{Env: src, Store: store}`.
  - `cmd/mcp.go`: `secretSetting` for `SchemeStore` asks `vault.Has(ctx, ro, secrets.ScopeNode, name)`.
  - The lab (`internal/chatlab/node.go`): `MasterKey: filepath.Join(n.dir, "master.key")`.
  - Docs: in `03-components.md` draw the vault between the module host and the database, and
    between the command line and the database; in `05-code-map.md` add the `secrets/vault` row
    and update the rows of `secrets`, `modhost`, `sandbox`, `config`, the binary, and the
    dependency rules (`secrets/vault` imports `secrets`, `sqlitedb`, `status`, `texts`;
    `settings` does not import `secrets/vault`).
- [x] **Step 4: `make lint test`; commit, push.**

```bash
git commit -m "config: modules read their own scope of the secret store, then the environment (R-CFG-9)"
```

---

### Task 7: `cmd` — `ganglion secret set|rm|list`

**Files:**
- Create: `cmd/secret.go`, `cmd/secret_test.go`
- Modify: `cmd/root.go`, `docs/architecture/05-code-map.md`

**Interfaces:**
- Consumes: `flags.DBFlag`, `vault.LoadKey`, `vault.KeyPath`, `vault.Open`, `(*Vault).Set`,
  `Remove`, `List`, `Locked`, `vault.MaxValue`, `secrets.ModuleScope`, `secrets.ScopeNode`,
  `modstore.LocalPrefix`, `modstore.IsLocal`.
- Produces: `func secretCommand(in io.Reader, out io.Writer) *cli.Command`, added to
  `newRootIO`'s `Commands`.

Usage:

```
ganglion secret set  [--db <file>] [--module <name>] NAME   # the value on standard input
ganglion secret rm   [--db <file>] [--module <name>] NAME
ganglion secret list [--db <file>]
```

`--module deye-reader` and `--module local/deye-reader` are the same module; without
`--module` the scope is `node`.

- [x] **Step 1: Failing tests** (`newRootIO(strings.NewReader(value), &out, &errOut, nil)`, a
  database in `t.TempDir()`):
  - `secret set --module deye-reader DEYE_APP_ID` with `"abc123\n"` on standard input stores
    `abc123` in `module:local/deye-reader`; the output is one line naming the scope and the
    name, **without the value**; `master.key` appears beside the database;
  - a value given as a second argument is refused with a usage error that says the value comes
    on standard input, and nothing is stored;
  - empty input, input of only whitespace, and input over 4096 bytes are refused; the reader
    is read to at most 4097 bytes (an endless reader does not hang the test);
  - a name that fails `secrets.ValidName` is refused;
  - `secret list` prints scope, name, date and who, sorted, and no value; an empty store
    prints one line saying so;
  - `secret rm` of a stored secret says it is removed; of an unknown one says there is none,
    and both exit without an error;
  - with a wrong `GANGLION_MASTER_KEY` on a store that has secrets: `set` fails with a message
    that says the store is locked and names `/secrets reset`; `list` still lists and adds the
    same line; **the rows are unchanged**;
  - the command does not take the database lock: with `node.LockDB` held by the test, `set`
    still succeeds (ruling 2);
  - **abuse:** the value never appears in `out`, `errOut` or any returned error across every
    case above (one helper asserts it).
- [x] **Step 2: Run, see them fail:** `go test ./cmd -run Secret`.
- [x] **Step 3: Implement.** Read with `io.LimitReader(in, vault.MaxValue+1)`; trim surrounding
  whitespace as `secrets.Env` does. `by` is `"cli"`. The command reads the environment through
  `secrets.OSEnv()` for the operator's key only; it loads no settings file. Help text: say the
  value is read from standard input and show `printf %s "$VALUE" | ganglion secret set …` and
  `ganglion secret set … < file`.
- [x] **Step 4: `make lint test`; commit, push.**

```bash
git commit -m "cmd: ganglion secret set, rm and list — a value on stdin, no restart (R-CFG-9)"
```

---

### Task 8: `agent` — `/secrets`

**Files:**
- Create: `agent/secrets_cmd.go`, `agent/secrets_cmd_test.go`
- Modify: `agent/commands.go`, `agent/agent.go`, `texts/texts.go`,
  `texts/lang/{en,ru,uk}.yaml`, `config/agent.go`, `docs/architecture/05-code-map.md`

**Interfaces:**
- Consumes: `vault.Entry`, `(*Vault).List`, `Remove`, `Reset`, `Locked`.
- Produces:

```go
// agent

// SecretsAdmin is what /secrets does with the store (vault.Vault).
type SecretsAdmin interface {
	List(ctx context.Context) ([]vault.Entry, error)
	Remove(ctx context.Context, scope secrets.Scope, name string) (bool, error)
	Reset(ctx context.Context) (int, error)
	Locked() bool
}

// NoSecrets is a node without a store: nothing listed, nothing removed.
type NoSecrets struct{}

type Deps struct {
	// ...
	// Secrets is what /secrets reads and changes; NoSecrets when unset.
	Secrets SecretsAdmin
}
```

Commands, for an admin in their private chat, with no model:

| Command | Does |
|---|---|
| `/secrets` | Lists names by scope with the date each was set; says when the store is locked |
| `/secrets rm <module> NAME` | Removes a module's secret (`<module>` with or without `local/`) |
| `/secrets rm node NAME` | Removes one of the node's |
| `/secrets reset` | Says how many secrets would be dropped and asks for `/secrets reset confirm` |
| `/secrets reset confirm` | Drops them all and adopts the node's key |

New text keys (write all three languages; English here):

```yaml
secrets.private_only: "Ask /secrets in our private chat."
secrets.empty: "No secrets are stored. On the host: ganglion secret set --module <name> NAME"
secrets.head: "Stored secrets. Values are never shown."
secrets.scope_node: "The node's own"
secrets.row: "{name}, set {date}"
secrets.locked: "The store is locked: its key does not fit these secrets. They cannot be read. /secrets reset drops them all."
secrets.removed: "{scope}: {name} is removed."
secrets.not_found: "{scope} has no secret {name}."
secrets.reset_ask: "This drops all {count} stored secrets; each must be set again. Send /secrets reset confirm to go on."
secrets.reset_done: "Dropped {count} secrets. The store is open on the node's key."
secrets.usage: "Usage: /secrets, /secrets rm <module|node> NAME, /secrets reset"
```

The command hint: `"Stored secrets: /secrets, /secrets rm <module|node> NAME"`, in the three
languages of `commandTable`.

- [x] **Step 1: Failing tests** (the agent's command tests, as `agent/modules_test.go` and the
  `/memory` tests do them, with a fake `SecretsAdmin`):
  - a person who is not an admin gets `reply.not_available`; an admin in a group gets
    `secrets.private_only`; neither calls the store;
  - `/secrets` with two scopes lists `deye-reader` then the node's own, each name with its
    date in the node's zone (`dateLayout`); names are escaped (`texts.Escape`);
  - an empty store answers `secrets.empty`; a locked store puts `secrets.locked` first and
    still lists;
  - `/secrets rm deye-reader DEYE_APP_ID` calls `Remove` with `module:local/deye-reader`;
    `/secrets rm node X` with `node`; found and not found give their texts; a missing name
    gives the usage;
  - `/secrets reset` does **not** call `Reset` and names the count; `/secrets reset confirm`
    calls it once and names the count it returned; `/secrets reset now` gives the usage;
  - the history: the list leaves no note; a removal and a reset leave a `[command]` note that
    names the scope and the secret's name and nothing else;
  - `/secrets` is in `Commands()` with its hint; the turn's outcome is `OutcomeCommand` and no
    model is called.
- [x] **Step 2: Run, see them fail:** `go test ./agent -run Secrets`.
- [x] **Step 3: Implement.** Follow `agent/memory_cmd.go` for the admin and private-chat
  checks and `agent/modules.go` for the reply-and-note pair. The verbs are a dispatch table
  (conventions §4), not a growing `switch`. Add `withSecrets(d Deps)` beside `withMemory`.
  `config/agent.go` passes `do.MustInvoke[*vault.Vault](i)`.
- [x] **Step 4: `make lint test`; commit, push.**

```bash
git commit -m "agent: /secrets lists names, removes one and resets a locked store (R-CFG-9, R-CFG-10)"
```

---

### Task 9: acceptance and docs

**Files:**
- Create: `tools/chatlab/scenarios/secrets.yaml`, `config/secrets_accept_test.go`
- Modify: `docs/deploy-railway.md`, `docs/conventions.md` (§8's secrets row),
  `docs/architecture/05-code-map.md`, `docs/README.md`, `docs/plans/p1-all-in-one.md`,
  `README.md` if it lists the commands

- [x] **Step 1: The acceptance test** (`config/secrets_accept_test.go`; a node container on a
  database **file** in `t.TempDir()`, the probe module installed as the module-host tests
  install it; this is the phase plan's "done when" of task 6.5, one subtest each):
  - *no restart:* the module's `secret.get` is unset; a second `vault.Open` on the same file
    with the same key file (what `ganglion secret set` does) sets it; the module's next call
    gets the value, with nothing restarted;
  - *no other module:* a second module whose manifest names the same secret gets nothing;
  - *a database copy without the key:* copy the database file to another folder, `LoadKey`
    there makes a new key, `Open` is locked, and `Get` returns `""`; a scan of the copied
    file's bytes does not find the value;
  - *the wrong key:* the node container started on the copy starts, its `/status` has the
    locked line, and the module's secret reads as unset.
- [x] **Step 2: The lab scenario** (`tools/chatlab/scenarios/secrets.yaml`, scripted model):
  `/secrets` on a fresh lab node answers that nothing is stored; a non-admin peer gets the
  not-available reply. Follow an existing command scenario for the shape; run it with the
  make target the other scenarios use.
- [x] **Step 3: Docs.**
  - `deploy-railway.md`: a section "Module secrets": moving one from a Railway variable into
    the store (`railway ssh`, then `printf %s "$DEYE_APP_ID" | ganglion secret set --module
    deye-reader DEYE_APP_ID`), that the variable can be deleted after `/secrets` shows the
    name, and that `master.key` lies on the volume and must be kept with it.
  - `conventions.md` §8: the row "Secrets from the environment and `*_FILE`" also names
    `secrets/vault` for stored ones.
  - `05-code-map.md`: every row touched by tasks 6–8 is final; the `secrets` row no longer
    says "planned".
  - `docs/README.md`, "Where we are": task 6.5 closed, with the date; what remains open (the
    intake from the chat, spec section 11).
  - `p1-all-in-one.md`: task 6.5 marked **Done** with the date and a link to this plan.
- [ ] **Step 4: The live check on Railway** (the owner's, recorded in `docs/README.md`): one
  of `deye-reader`'s four secrets moved into the store, its variable deleted, the module's
  next run healthy, `/secrets` showing the name.
- [x] **Step 5: `make lint test`; commit, push.**

```bash
git commit -m "secrets: acceptance for the store, the Railway steps, task 6.5 closed"
```

---

## Notes from the implementation (2026-11-13)

Where the code differs from the tasks above:

- **`sqlitedb.Migrate` is safe for two processes** (task 4's commit): each
  migration is applied under the write lock, with the version checked again
  there. Ruling 2 lets a command open the store beside a starting node, and
  both then migrate the `secrets` component.
- **The key file appears by a hard link** from a temporary file, so a reader
  never sees it half written; a file system without hard links gets it
  written in place, created exclusively.
- **`run` logs no warning of its own for a locked store.** The node's start
  says the store is locked (`vault.Announce`) and `run` says which optional
  part is off for want of its `store:NAME`.
- **`ganglion secret set` reads 64 bytes past the longest value**, so a
  value of 4096 bytes with its newline is stored and an endless input still
  ends.
- **`set` makes a database that is not there** (a secret stored before the
  node's first start); `rm` and `list` refuse a path with no database.
- **`modhost.OneSource` and `modhost.NothingToForget`** exist for a host
  without a store.
- **The refusal of an unset secret at install** now says how the owner sets
  one (`modstore`), so the agent can pass it on.
- **The lab scenario is `mech-secrets`**; the lab has one peer, so the
  refusal of a person who is not an admin is held by the agent's unit test.
- **Task 6.5 is not closed:** the merge and step 4 of task 9, the owner's
  live check on Railway, are left.

## What this plan leaves out

- Handing a value over from the chat (the request, the page, the return channel): a spike on
  Telegram's `sendData`, then its own ADR (spec section 11).
- Moving the claude CLI's token into the store (`/login`, board card G-151).
- Rotating the master key; values over 4 KB; secrets for a runner on another machine (P2).
- A tool that lets the agent ask for a secret: it arrives with the intake.
