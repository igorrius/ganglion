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
  type ToolFunc func(ctx context.Context, call provider.ToolCall) (string, error)
  type ToolMiddleware func(next ToolFunc) ToolFunc
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

## 8. One source for each piece of knowledge

Before writing a helper, look for it. These exist and are the only copies:

| Knowledge | Where |
|---|---|
| Backoff with jitter | `internal/backoff` |
| Truncating text by runes | `internal/text` |
| Rendering history as a plain transcript | `internal/transcript` |
| Start of the day in a location | `internal/clock` |
| Cost of a call at catalogue prices | `router.Candidate.Cost` |
| Alerts to every admin's private chat | `alert` |
| Principal → private conversation | the transport (`telegram.PrivateConv`) |
| Secrets from the environment and `*_FILE` | `secrets` |
| Version, commit, toolchain of this binary | `internal/buildinfo` |

(The table describes the state after the [refactoring plan](plans/p1-refactor.md).)

## 9. Tests

- Table tests for pure functions; behaviour tests through the public API.
- Fakes live in `<pkg>test` packages (`providertest`, `telegramtest`) and
  are shared, not rewritten per test.
- No sleeping: inject clocks and `after` functions.
- `config` has a test that builds every role's container with fakes, so a
  missing provider fails in CI, not at start.

## 10. Comments

English. A doc comment states the contract: what the function does, what
its errors mean, and the spec requirement (`R-*`) or ADR behind a rule that
is not obvious. Comments say why; the code says how.
