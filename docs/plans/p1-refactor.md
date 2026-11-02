# P1 · Refactoring before M3b — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:executing-plans
> to implement this plan task by task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** bring the M1–M3a code in line with the
[Go conventions](../conventions.md) before M3b builds memory, tools,
policies and skills on it. The node is assembled in one place (`config`,
`samber/do`) and started through a real command line (`urfave/cli`).

**Decisions:**
- [ADR-0027](../adr/0027-written-go-conventions-and-lint-guardrails.md):
  conventions and lint guardrails;
- [ADR-0028](../adr/0028-samber-do-composition-root-in-config.md):
  `samber/do` composition root;
- [ADR-0029](../adr/0029-urfave-cli-for-commands-and-bootstrap-flags.md):
  urfave/cli v3.

## Global constraints

- **No behaviour change.** These stay as they are:
  - replies and log lines (keys and messages);
  - metric names and labels, span names;
  - the database schema and the state JSON;
  - environment variable names.

  The one visible change is `ganglion run` (ADR-0029).
- **Tests stay green on every commit.** `make check` passes after each
  task. Tests change only where an API they call moves; their assertions do
  not weaken.
- **Pinned versions.**
  - `github.com/samber/do/v2` v2.1.0 (2026-07-20);
  - `github.com/urfave/cli/v3` v3.14.0 (2026-10-02).
- **Memory.** Binary size and RSS after 1,000 turns (`cmd/ganglion`
  memory test) are measured before task 1 and after task 7. The numbers go
  into this plan.
- **Out of scope.** M3b features, the YAML node config (D-3), and new
  commands beyond `run` and `version`. The deferred minors of the M2 and
  M3a reviews are done only where a task touches that code anyway.

## Baseline

| Measure | Before | After |
|---|---|---|
| `bin/ganglion` size | 32.8 MB | |
| RSS idle / after 1,000 turns (echo) | 26.2 / 33.3 MB | |
| RSS idle / after 1,000 turns, peak (agent) | 26.4 / 34.3 MB, 34.9 MB | |
| Lines of Go outside tests | 9,540 | |

### Task 1: typed vocabulary — done

**Files:** `provider/provider.go`, `turns/turns.go`, `event/event.go`,
`router/router.go`, `router/rules/*.go`, `agent/*.go`,
`provider/claudecli/claudecli.go`, `provider/openai/client.go`,
`transport/telegram/transport.go`, `core/*.go`, `access/access.go`

- [x] `provider.Role` with `RoleSystem`, `RoleUser`, `RoleAssistant`,
  `RoleTool`. `Message.Role` takes this type; the JSON stays the same
  string.
- [x] `turns.Outcome` with `OutcomeOK`, `OutcomeStepLimit`,
  `OutcomeDailyLimit`, `OutcomeNoModel`, `OutcomeCommand`, `OutcomeError`.
- [x] `event.Kind` with `KindMessage`, used by the agent, `core`, `access`
  and the transport.
- [x] `router.Tier` with `TierCheap` and `TierSmart`. `router.TaskKind` for
  the task kinds (`message`, `compaction`).
- [x] A `Name` constant for the Telegram transport, used instead of
  `"telegram"` in `node` and the transport.
- [x] `make check`; commit.

### Task 2: one source for shared knowledge

**Files:** new `internal/backoff`, `internal/text`, `internal/transcript`,
`internal/clock`, `alert`, `secrets`; changed `outbox/sender.go`,
`transport/telegram/transport.go`, `agent/*.go`, `provider/claudecli`,
`router/router.go`, `router/rules/rules.go`, `node/*.go`,
`provider/errors.go`

- [ ] `internal/backoff`: a doubling backoff with ±20% jitter and a cap
  (`Next`, `Reset`), plus `Delay(attempt)`. The outbox and the Telegram
  transport use it. Their existing tests pin the timings.
- [ ] `internal/text.Truncate(s, n)`, used by `agent` and `claudecli`.
- [ ] `internal/transcript.Render(msgs, opts)`: "User: … / Assistant: … /
  Tool result: …". Compaction and the claude CLI prompt both use it. The
  claude CLI keeps tool calls in its output and compaction leaves them out
  through an option. Tests pin both outputs byte for byte.
- [ ] `internal/clock.DayStart(t, loc)`, used by the agent's limits and the
  router's budget.
- [ ] `router.Candidate.Cost(provider.Usage) float64`, used by the turn
  trace and the cost metric.
- [ ] `telegram.PrivateConv(principal)`: moved from `node.telegramPrivate`.
- [ ] Package `alert`: `ToAdmins(ctx, admins AdminsFunc, private
  PrivateConvFunc, key, text)` returns the outgoing messages. The daily
  limit alert, the give-up alert and the `409` alert use it. A failure to
  list admins is returned, not dropped. This is the M2 minor about the
  `409` alert: the caller logs and goes on.
- [ ] Package `secrets`: `Source` interface and `Env` (the variable or
  `<NAME>_FILE`, trimmed, one of the two). Moved from `node.secret` with its
  tests.
- [ ] Remove `provider.Fallback` (unused; the agent falls back on any
  error, spec 7.3 layer 3). This is the M3a minor.
- [ ] `make check`; commit.

### Task 3: the agent as a stage pipeline

**Files:** `agent/agent.go` split into `agent/turn.go`, `agent/stages.go`,
`agent/commands.go`, `agent/model.go`, `agent/limits.go`; `agent/tools.go`,
`agent/sessions.go`, `agent/compact.go`, `agent/prompt.go`

- [ ] `turn` struct: the event, the decoded message, the state, the trace
  record, the replies, the clock reading. `reply(text)` and `finish()`
  become methods on it, not closures.
- [ ] `type stage func(ctx, *turn) (done bool, err error)`. `Handle` runs
  the list `decode → idle session → commands → daily limits → answer →
  compact`, then `finish`. `Handle` itself stays under 30 lines.
- [ ] Commands as a table: `map[string]command`, where a command is
  `func(ctx, *turn, arg string) error`, with `/new`, `/reset`, `/sessions`
  and `/resume`. `sessions()` with seven arguments goes away.
- [ ] `answer` stage: the model and tool loop. Model calls go through
  `call(ctx, task, msgs, tools) (resp, ok)`, which records attempts on the
  turn. Tool calls go through a `ToolFunc` wrapped by middleware. Tracing
  becomes the first `ToolMiddleware`; M3b adds policies and the depth limit
  as more middleware.
- [ ] `Deps` uses named types: `StateReader` interface, `AdminsFunc`,
  `PersonaFunc`, `PrivateConvFunc`.
- [ ] `Config`: `DefaultConfig()` returns today's defaults; zero turns a
  limit off. The `-1` sentinel and `node`'s 0→-1 mapping go away.
- [ ] `systemPrompt`: one clear path. An error logs and uses the default.
- [ ] The existing `agent` tests pass unchanged except for constructors.
  New table tests cover the command table and the stage order (a stage
  that ends the turn skips the rest).
- [ ] `make check`; commit.

### Task 4: the `config` package with `samber/do`

**Files:** new `config/` (`bootstrap.go`, `core.go`, `storage.go`,
`delivery.go`, `agent.go`, `providers.go`, `telemetry.go`,
`config_test.go`); `node/node.go` reduced to the role runner; `go.mod`

- [ ] `go get github.com/samber/do/v2@v2.1.0`.
- [ ] `config.Bootstrap`: today's `node.Config` and `AgentConfig` fields as
  plain settings, with `Validate()`. The startup context is provided as a
  value. Until task 5, `cmd/ganglion` fills `Bootstrap` from the
  environment, so the flags change in one task.
- [ ] One provider per component, each a function that returns
  `do.Provider[T]`:
  - lock, DB, queue, access list, turns log, archive;
  - router, providers, agent, handler;
  - sender, Telegram, loop, prune.
- [ ] The provider factory becomes a table from the catalogue's provider
  type (`rules.TypeOpenAI`, `rules.TypeClaudeCLI`) to
  `func(rules.ProviderDef, secrets.Source) (provider.Provider, error)`.
- [ ] `Shutdown(ctx) error` on the lock and the DB wrapper, so the
  container closes them in reverse order. Telemetry stays with
  `cmd/ganglion` until task 5.
- [ ] `node.Component` (`Name() string`, `Run(ctx) error`) and
  `node.RunAll`. `config.CoreComponents()` provides the list for the core
  role: sender, loop, prune, and Telegram when a token is set. The typing
  hook is wired in `config`.
- [ ] Extension points are provided as interfaces whose provider picks
  the implementation:
  - `core.Handler`: the agent or the echo;
  - the transports: Telegram or none;
  - the turn hook: Telegram typing or a no-op;
  - the session archive: SQLite or a no-op.

  The `nil` checks in `agent` and `node` go away.
- [ ] `do.Package` per role: `config.Core`, plus `config.Edge` and
  `config.Worker`, which are empty for now.
- [ ] `config_test.go` builds the core container with the fake Bot API and
  `providertest`, with and without a model catalogue. It invokes the
  components and runs one echo turn and one agent turn end to end.
- [ ] `make check`; commit.

### Task 5: `urfave/cli` commands and flags

**Files:**
- new: root `main.go`, `cmd/` (`root.go`, `run.go`, `version.go`,
  tests), `cmd/flags/`;
- moved: `cmd/ganglion` tests into `cmd`; `cmd/modelcompare`,
  `cmd/modrun` and `cmd/modbench` to `tools/`;
- removed: `node/config.go`, `node/config_test.go`;
- changed: `Makefile`, `.github/workflows/ci.yml` if paths appear there,
  `go.mod`.

- [ ] `go get github.com/urfave/cli/v3@v3.14.0`.
- [ ] `cmd/flags`: one flag per bootstrap setting, each with:
  - `Sources: cli.EnvVars(<today's variable>)`;
  - today's default as `Value`;
  - `Destination` into `config.Bootstrap`;
  - a `Validator` where today's code checks the value;
  - a `Category` for `--help`.

  `run`'s flags are `Local`, and `--log-level` sits on the root. Secrets
  are not flags: they come from `secrets.Env`.
- [ ] Move the development tools to `tools/` as they are. Update their
  `Makefile` targets and the docs that name their paths.
- [ ] `ganglion run` (`--roles`, the flags above, `GANGLION_LOG_LEVEL` as
  `--log-level`). It does three things:
  - sets up telemetry;
  - builds the container from `Bootstrap` and the role packages;
  - runs `node.RunAll` until a signal, then `injector.ShutdownWithContext`.
- [ ] `internal/buildinfo`: `Info{Version, Commit, CommitTime, Modified,
  Go, OS, Arch}` read once. `Read()` takes, in order:
  - the `-X` version, if set;
  - the module version from `debug.ReadBuildInfo`, unless it is
    `(devel)`;
  - otherwise `dev`.

  Commit, commit time and the dirty flag come from the `vcs.*` settings.
  A table test feeds `debug.BuildInfo` values.
- [ ] `ganglion version` prints every field as `key: value`, or JSON with
  `--json`. `ganglion --version` prints `ganglion <version> (<short
  commit>[, dirty])`. Root without a command prints help. The root
  `main.go` holds only signal handling and `cmd.Execute`.
- [ ] The start log line and the telemetry resource carry the version and
  the commit from `buildinfo`. The resource gets the commit as
  `vcs.ref.head.revision`, so Prometheus' `target_info` shows which build
  runs.
- [ ] `Makefile`: `-X github.com/igorrius/ganglion/internal/buildinfo.version=$(VERSION)`.
  A test builds the binary with and without `-X` and checks the
  `version` output.
- [ ] Tests: every old environment variable still sets its setting;
  `--help` lists them; a bad value fails with the variable's name. The
  crash harness and the memory test call `run --roles core`.
- [ ] `Makefile`: `live-echo` and `live-agent` use `ganglion run --roles
  core`.
- [ ] `make check`; commit.

### Task 6: lint guardrails

**Files:** `.golangci.yml`, code that the new linters flag

- [ ] Enable `funlen` (60 lines, 40 statements), `gocognit` (20), `dupl`
  (150 tokens), `goconst`, `exhaustive`. Tests are excluded from `funlen`
  and `dupl`.
- [ ] `depguard`:
  - `github.com/samber/do` only in `config`;
  - `github.com/urfave/cli` only in `cmd/...`.
- [ ] Fix what they find. A `//nolint:<linter> // <reason>` is allowed for
  protocol `switch`es and generated-shape code.
- [ ] `make check`; commit.

### Task 7: measure, run live, close

- [ ] Fill in the baseline table: binary size, RSS after 1,000 turns,
  lines of Go.
- [ ] `make live-agent` on the test bot: a turn, `/new`, `/sessions`,
  `/resume`, the daily limit with a low `GANGLION_DAILY_TURNS`, a stop with
  Ctrl-C. Then the owner runs it.
- [ ] Docs in the same commit:
  - [code map](../architecture/05-code-map.md): `config`, `internal/*`,
    `alert`, `secrets`, `node` as the role runner;
  - the dependency diagram, with the rule "only `config` wires";
  - [containers](../architecture/02-containers.md), if the start command
    is shown there;
  - "Where we are" in [docs/README.md](../README.md).
- [ ] A code review of the branch against [the conventions](../conventions.md).
