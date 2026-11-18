# Go conventions

How code in ganglion is written. Read this before writing or changing Go
code; it holds for people and for models alike. The reasons are in
[ADR-0027](adr/0027-written-go-conventions-and-lint-guardrails.md); the
composition root is [ADR-0028](adr/0028-samber-do-composition-root-in-config.md)
and the command line is [ADR-0029](adr/0029-urfave-cli-for-commands-and-bootstrap-flags.md).

The linter enforces what it can (`.golangci.yml`); the rest is checked in
review against this page. When a rule here is wrong, change the page in the
same commit as the code, not the other way round.

## 1. Packages and dependencies

- **Interfaces in small packages, implementations in their own.**
  `transport`, `queue`, `provider` and `router` hold types and interfaces;
  `transport/telegram`, `queue/sqlqueue` and others implement them. The
  [dependency rules](architecture/05-code-map.md#dependency-rules) say who
  may import whom.
- **One composition root.** Only the `config` package builds concrete
  implementations and connects them, through `samber/do` providers
  (ADR-0028). Domain packages never import `github.com/samber/do`.
- **`config` holds dependency logic only.** A provider takes what its
  component needs with `do.MustInvoke` when an error is only passed on, and
  with `do.Invoke` plus handling when a decision depends on it (ADR-0030), maps settings to
  constructor arguments and calls the constructor. An extension point is
  an interface provider that only selects (`choose`); each implementation,
  the no-op included, is registered under its own name constant. No texts, goroutines, defaults, policies or log lines
  about the product in `config`: they belong to the package that owns the
  behaviour (the agent's give-up reply, Telegram's typing hook, the
  queue's lease release, the access list's admin seed).
- **One command line.** `main.go` at the root only calls `cmd.Execute`.
  The command tree lives in `cmd` and the flags in `cmd/flags`. Only
  `cmd/...` imports `github.com/urfave/cli/v3` (ADR-0029). `config` gets
  a plain `config.Bootstrap` value. Domain packages never read flags or
  environment variables.
- **Development tools live in `tools/`,** not `cmd/`: they are not
  shipped.
- **`internal/` for shared helpers.** Code shared by several packages but
  not part of the library API (backoff, text helpers, transcript rendering)
  goes under `internal/`, so embedding ganglion as a library (spec
  principle 5) does not freeze it.
- **No package-level mutable state.** OpenTelemetry's global providers are
  the one exception (ADR-0026).

## 2. Constructors and dependencies

- A constructor takes its required collaborators as arguments and optional
  settings as functional options: `New(q queue.Queue, opts ...Option)`.
- A dependency is the smallest interface the consumer needs, declared by the
  consumer, or a **named function type**:

  ```go
  // AdminsFunc lists the admin principals.
  type AdminsFunc func(ctx context.Context) ([]string, error)
  ```

  No anonymous interfaces or unnamed `func` types in struct fields.
- **No `nil` dependencies.** An optional collaborator gets a no-op
  implementation. The `config` provider of the interface picks the real
  one or the no-op from the settings (ADR-0028). Domain code never checks
  `if x == nil` or `if x != nil` for a collaborator.
- A config struct is complete: `DefaultConfig()` returns the defaults and
  the caller overrides fields. Zero means zero (or "off"), never "use the
  default"; no `-1` sentinels.
- Time comes from an injected clock (`now func() time.Time`); logic never
  calls `time.Now` directly.

## 3. A closed set of values is a type

Roles, outcomes, event kinds, tiers, error kinds and transport names are
named string types with constants, never bare strings:

```go
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)
```

A `switch` over such a type lists every constant or says why not
(`exhaustive` linter).

## 4. Functions and flow

- **Small functions, one level of abstraction each.** The linter caps
  length (`funlen`: 60 lines, 40 statements) and cognitive complexity
  (`gocognit`: 20). Tests, shared test kits (`*test` packages) and
  `tools/` are exempt from these limits; production code is not. A function that reads like a list of steps calls
  functions named after the steps.
- **A process of several steps is a pipeline of named stages** over one
  state object, not a long function with closures:

  ```go
  // turn is everything one handled event needs and produces.
  type turn struct {
  	event  event.Event
  	msg    event.Message
  	state  State
  	record turns.Turn
  	out    []event.Outgoing
  }

  // stage does one step of a turn; done ends the turn early.
  type stage func(ctx context.Context, t *turn) (done bool, err error)
  ```

  The stage list is data (`[]stage{a.session, a.commands, a.limits,
  a.answer, a.compact}`), so a new step is a new function and one line.
- **Cross-cutting behaviour is middleware.** Policies, limits, tracing and
  audit wrap the thing they guard, the way `net/http` handlers are wrapped:

  ```go
  type Func func(ctx context.Context, c tool.Call) (tool.Result, error)
  type Middleware func(next Func) Func
  ```

  A policy is a function that decides; it does not edit the code it guards
  (spec principle 6: the executor enforces).
- **Dispatch tables, not growing switches.** Commands, provider factories
  and similar choices are maps from a key to a function, filled in one
  place.
- **Pure where possible.** Computation (cost, splitting, rendering,
  routing order) is a pure function of its inputs, tested without fakes;
  I/O sits around it.
- **Return values instead of mutating arguments,** except the state object
  of a pipeline.

## 5. Errors

- Wrap with the package and the action: `fmt.Errorf("sqlqueue: lease: %w", err)`.
- A caller that decides on an error gets a sentinel (`queue.ErrLeaseLost`)
  or a typed error (`*provider.Error` with a `Kind`); it never matches
  strings.
- Log an error or return it, not both. The place that decides what happens
  next logs it.
- `context.Canceled` on shutdown is not an error worth logging.

## 6. Goroutines and lifecycle

- A long-running part is a component with `Run(ctx context.Context) error`
  that returns when `ctx` ends. The node runs components and stops them
  together; no goroutine is started without an owner that waits for it.
- Resources with a lifetime (database, lock, telemetry) implement
  `Shutdown(ctx context.Context) error`; the `do` container closes them in
  reverse dependency order (ADR-0028).
- The startup context is for start-time work only (migrations, checks);
  running work uses the context passed to `Run`.

## 7. Observability

Instruments live in the package's `telemetry.go`. Labels are
low-cardinality (provider, model, outcome, kind, transport); never a
conversation, a principal or message text (ADR-0026).

**Everything significant is observable both ways, in the same commit.**
This means code that can fail, wait, retry, cost money or grow (a queue,
a context, a cache). The two ways answer different questions:

| Question | Answered by | Who reads it | Spec |
|---|---|---|---|
| How is the node doing: speed, errors, cost, load? | a **metric** | people, on the promtop dashboard, which reads only `/metrics` | R-OBS-1, R-OBS-8 |
| Why did this one go wrong? | a **log line** with the turn's ids and the trace ids, and a **journal row** for a tool call | a model or a person diagnosing it, through the log files and `ganglion mcp` | R-OBS-4–7 |

Neither replaces the other:
- a journal row is not on the dashboard;
- a counter does not say which turn failed.

Logs follow section 5 (log an error or return it); a log line carries
ids and kinds, never message text or secrets.

**Metrics** (R-OBS-8):

- **The kind follows the question:**
  - a counter by outcome for anything that can fail or be refused;
  - a histogram in base units (seconds, bytes, tokens) for anything whose
    time or size matters;
  - an observable gauge for a level (queue depth, pending
    confirmations).
- **Buckets come from `internal/buckets`** by base unit: `buckets.Seconds`,
  `Bytes`, `Tokens`, `Counts`. The SDK's default buckets suit
  milliseconds: a histogram in seconds without them puts every
  sub-second time in one bucket.
- **A count of rows is a gauge through `sqlitedb.ObserveCount`**: it
  registers its callback apart from the instrument, since the SDK keeps
  only the first callback passed when an instrument is made.
- **Label values come from a closed set:**
  - a named type's constants;
  - tool names from the registry, models from the catalogue, modules from
    the installed set.
  - Never a host, a URL, an id, an error message or any text.
- **Tests** read the new metric back through the Prometheus text, as the
  existing telemetry tests do.

## 8. One source for each piece of knowledge

Before writing a helper, look for it. These exist and are the only copies:

| Knowledge | Where |
|---|---|
| Backoff with jitter | `internal/backoff` |
| Truncating text by runes | `internal/text` |
| A Markdown code span that shows its text exactly, past any backtick inside | `text.CodeSpan` (`internal/text`) |
| Rendering history as a plain transcript | `internal/transcript` |
| Start of the day in a location | `internal/clock` |
| Cost of a call at catalogue prices | `router.Candidate.Cost` |
| Alerts to every admin's private chat | `alert` |
| A route the outside world can reach, and the check that the node's public address reaches this process | `publichttp` (`Handle`, `Reaches`); never the metrics listener |
| Principal → private conversation | the transport (`telegram.PrivateConv`) |
| Secrets from the environment and `*_FILE`; several sources read in order | `secrets` (`Env`, `First`) |
| Stored secrets: reading one scope as a `secrets.Source`, setting, listing names, never a value to a person | `secrets/vault` (`Vault.Source`, `Set`, `List`) |
| Making the process not dumpable, so its `/proc` files are shut to its user's other processes (the node, chatlab, tests) | `sandbox.Harden`, `sandbox.SetDumpable` |
| Version, commit, toolchain of this binary | `internal/buildinfo` |
| Comparing web addresses (lower-case host, an internationalised host in punycode, no fragment, no default port); finding them in text (`Find`, the one URL pattern), defanging text (`Defang`) | `internal/urlnorm` |
| Checking a call's arguments against the tool's declared parameters | `tool.CheckArgs` |
| Whether two calls' arguments are the same (canonical JSON) | `tool.CanonicalArgs` |
| Marking outside content as data for the model, and dropping it from a text (`StripExternal`) | `tool.WrapExternal` |
| Removing secrets before anything is journalled or logged | `redact.String` (`internal/redact`) |
| An id nobody can guess (button ids) | `event.RandomID` |
| A SQLite transaction that takes the write lock first | `sqlitedb.Immediate` |
| Running git in the workspace (fixed author, no user git settings, paths kept inside) | `gitrepo` |
| Histogram buckets by unit | `internal/buckets` (`Seconds`, `Bytes`, `Tokens`, `Counts`) |
| A count of rows in one state as a gauge | `sqlitedb.ObserveCount` |
| Recognising a pasted intake line (`gauth1.`/`gsec1.`): held anywhere in a text, or read alone | `intake` (`Holds`, `Parse`) |
| Asking a model whether a text is a fact or a hidden instruction | `guard` |

(The table describes the state after the [refactoring plan](https://github.com/igorrius/ganglion/blob/e6b25054290c0a0e460edf0639b5e8dfa761f251/docs/plans/p1-refactor.md).)

## 9. Tests

- Table tests for pure functions; behaviour tests through the public API.
- Fakes live in `<pkg>test` packages (`providertest`, `telegramtest`) and
  are shared, not rewritten per test.
- No sleeping: inject clocks and `after` functions.
- `config` has a test that builds every role's container with fakes, so a
  missing provider fails in CI, not at start.
- **A cached pass must mean the code still passes.** CI reuses `go test`'s cached
  results. They depend on the test's binary, the environment it reads and the files
  it opens or stats; `scripts/pin-file-times.sh` makes those files' times follow
  their contents. `go test` does not see what a child process reads, so a test that
  starts `go build` or `go list` calls `testinputs.Build` first. A test that depends
  on anything else outside its process (the network, the date) passes `-count=1` in
  its own make target and stays out of `go test ./...`. `gh workflow run CI --ref
  <branch>` tests everything with no cached result.

## 10. Comments

English. A doc comment states the contract: what the function does, what
its errors mean, and the spec requirement (`R-*`) or ADR behind a rule that
is not obvious. Comments say why; the code says how.

## 11. Security

Spec principle 9: every link is hostile, every input is untrusted. In
code that means:

- **Everything from outside the process is hostile.** That covers the
  network, files, a model's output, a module's result and another node's
  message.
  - Read it with limits on size, depth and time.
  - Turn it into typed values at the boundary.
  - Refuse unknown fields where the contract is ours (`strictJSON`,
    `tool.CheckArgs`).
- **Every network call** has:
  - a timeout and a cap on the body it reads;
  - verified TLS: never `InsecureSkipVerify`;
  - no credentials in a URL.
- **Listeners.** A listener binds to loopback unless the spec says
  otherwise. A peer proves who it is:
  - NATS credentials between nodes;
  - a token compared with `subtle.ConstantTimeCompare`;
  - a Unix socket readable only by the node's user.
- **Secrets** come from `secrets`. They are never written anywhere else:
  - not into logs, spans or metric labels;
  - not into the journal or the logs (`redact.String`);
  - not into an error a model reads;
  - not into a child process's argv.
- **Randomness** that someone could guess or replay comes from
  `crypto/rand` (`event.RandomID`); `math/rand` is for jitter only.
- **Deny by default.** A change that widens a right is the owner's
  decision and comes with a test.
- **An abuse test** goes with every change that crosses a trust boundary.
  It feeds the hostile input and asserts what must not happen.
