# ADR-0029: Use urfave/cli v3 for commands and bootstrap flags, with `main.go` at the root and the command tree in `cmd`

- **Status:** Accepted
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [2, principles 7 and 8](../spec.md#2-principles), [5.1](../spec.md#51-roles-not-machines); decision D-3 in the [P1 plan](https://github.com/igorrius/ganglion/blob/e6b25054290c0a0e460edf0639b5e8dfa761f251/docs/plans/p1-all-in-one.md#decisions-to-take-during-p1); [ADR-0028](0028-samber-do-composition-root-in-config.md)

## Context

`cmd/ganglion` parses two flags with the standard `flag` package:
`--roles` and `--version`. All other settings come from environment
variables. `node.ConfigFromEnv` reads them by hand: about 170 lines of
`getenv`, defaults, parsing and error messages.

The environment variables are:
- `GANGLION_DB`, `GANGLION_ADMIN`, `GANGLION_TZ`, `GANGLION_MODELS_FILE`;
- the `GANGLION_DAILY_*` limits, `GANGLION_SESSION_IDLE`,
  `GANGLION_METRICS_ADDR`, `GANGLION_TELEGRAM_REPLY`,
  `GANGLION_LOG_LEVEL`;
- `TELEGRAM_*`, and secrets with `*_FILE`.

The settings are not listed anywhere a person can see them; `--help` shows
two flags.

What is coming:
- **Commands beyond running a node.** Managing the access list (the M2
  review asked for admin removal), checking a model catalogue, and the
  development tools.
- **D-3 (M4)** separates three kinds of input:
  - bootstrap settings from flags or the environment;
  - the node config in YAML;
  - secrets from a secret source, never on the command line.

The repository has four `main` packages today:
- `cmd/ganglion`, the node;
- `cmd/modelcompare`, `cmd/modrun` and `cmd/modbench`, development tools
  for P0 and D-6.

The owner's other services use this layout:
- `main.go` at the repository root;
- a `cmd` package that builds the command tree;
- the flag definitions in `cmd/flags`;
- a CLI library that declares each flag together with where its value
  comes from (flag, environment variable, file). They prefer it to Cobra.

## Options: the library

### A. Standard `flag` and hand-written environment reading

Today's approach. No dependency. There are no subcommands without
hand-written dispatch, and each setting is declared twice: a field and a
`getenv` block.

### B. Cobra (with Viper for the environment)

The most common choice: commands, subcommands and generated help. Reading
the environment per flag needs Viper or hand-written binding. Viper brings
a large dependency tree and its own global configuration model.

### C. urfave/cli v3

v3.14.0 is from 2026-10-02 and has no runtime dependencies outside the
standard library. Each flag is one declaration with:
- `Sources: cli.EnvVars("GANGLION_DB")` for where the value comes from;
- `Value` for the default and `Destination` for the target, a pointer
  into a settings struct;
- `Validator` for a check;
- `Category` for a heading in `--help`.

Flags apply to subcommands unless marked `Local`. Commands nest, and
`MutuallyExclusiveFlags` and shell completion are built in.

### D. Kong

Commands and flags declared as struct tags, with environment variables
through tags. It is compact, but less familiar to the owner, and a flag's
behaviour lives in tag strings.

| Criterion | A. flag | B. Cobra + Viper | C. urfave/cli v3 | D. Kong |
|---|---|---|---|---|
| Subcommands | Hand-written | Yes | Yes | Yes |
| A flag, its variable, default and check in one place | No | Via Viper | Yes | Yes (tags) |
| Runtime dependencies | None | Large (Viper) | None | None |
| The owner's preference | — | No | Yes | — |

## Options: the layout

### L1. The owner's layout: root `main.go`, `cmd`, `cmd/flags`

- `main.go` at the root calls `cmd.Execute(ctx, os.Args)`.
- Package `cmd` builds the tree: root, `run`, `version`, and later
  `access` and `models`.
- `cmd/flags` declares the flags, bound to `config.Bootstrap` through
  `Destination`.
- `go install github.com/igorrius/ganglion@latest` gives a `ganglion`
  binary, and `go run .` starts it.

The development tools cannot stay under `cmd/` as separate `main`
packages: `cmd` is now an ordinary package, and binaries nested inside it
read as part of it. They move to `tools/`.

### L2. Go's multi-binary layout: `cmd/ganglion`, `internal/cli`

`cmd/<binary>/main.go` stays thin, as in many Go projects. The tree and
the flags go into `internal/cli` and `internal/cli/flags`.
- The install path stays `…/cmd/ganglion@latest`.
- The development tools stay where they are.
- It differs from the owner's other services.

| Criterion | L1. Root `main.go` + `cmd` | L2. `cmd/ganglion` + `internal/cli` |
|---|---|---|
| Install and run | `go install …/ganglion@latest`, `go run .` | `go install …/ganglion/cmd/ganglion@latest` |
| Same as the owner's other services | Yes | No |
| Where the CLI lives | `cmd`: one obvious place | `internal/cli`: one more name |
| Development tools | Move to `tools/` | Stay in `cmd/` |
| Command tree testable in process | Yes (`cmd.Root().Run`) | Yes |

## Decision

**C, urfave/cli v3, in layout L1.** Each flag and its environment variable
are declared in one place, the library has no runtime dependencies, and
both the library and the layout are what the owner already uses. The
development tools are not shipped to users, and `tools/` says so better
than `cmd/` did.

- **Layout.**
  - `main.go` at the root: signals and `cmd.Execute`.
  - `cmd/`: the command tree, and turning parsed flags into
    `config.Bootstrap`.
  - `cmd/flags/`: the flag declarations, grouped by `Category` (storage,
    Telegram, agent, telemetry).
  - `tools/modelcompare`, `tools/modrun`, `tools/modbench`: the
    development tools, moved as they are.
- **Commands.**
  - `ganglion run` runs a node; its `--roles` flag keeps the meaning of
    ADR-0004.
  - `ganglion version` prints what the binary is. `ganglion --version`
    prints the version line alone.
  - Without a command, `ganglion` prints help.
  - Later commands (`access`, `models check`) are added when their
    milestone needs them.
- **Flags.**
  - Every bootstrap setting is a flag with its current environment
    variable as the source. `.env` files, `make live-*` and hosts keep
    working with the same names.
  - Defaults and checks live in the flag declarations (`Value`,
    `Validator`), so `--help` shows the defaults and a bad value fails
    with the flag's name.
  - Settings every command needs, such as `--log-level`, sit on the root.
    Settings of `run` are `Local`.
- **Build information.** One source, `internal/buildinfo`, read by
  `version`, the start log line and the telemetry resource.
  - **Version:** the `-ldflags -X` value (`git describe` in the Makefile)
    when set. Otherwise the module version Go records for
    `go install …@vX.Y.Z`, or `dev` for a local build.
  - **Commit, commit time and dirty flag:** the VCS stamps Go writes into
    every build from a checkout (`vcs.revision`, `vcs.time`,
    `vcs.modified`). They work without `-ldflags`.
  - **Toolchain and platform:** Go version, OS and architecture.
  - **Output:** `version` prints one `key: value` line per field, or JSON
    with `--json` for scripts and bug reports.
- **Secrets are not flags.**
  - The bot token and the provider keys never appear on a command line
    (`ps` shows argv).
  - They come from the `secrets` source: the variable, or the file named
    by `<NAME>_FILE`.
  - D-3 later adds an encrypted store behind the same source.
- **Boundaries.**
  - `config` stays free of the CLI: it receives a plain
    `config.Bootstrap` value (ADR-0028).
  - `depguard` allows `github.com/urfave/cli` only in `cmd/...`.

## Consequences

- These go away:
  - `cmd/ganglion`;
  - `node.ConfigFromEnv` and its tests, replaced by the flag declarations
    and their tests.
- The `cmd/ganglion` tests move:
  - the tests of `run` and of the flags into `cmd`;
  - the crash harness and the memory test, which build the binary, into
    `cmd` as well, now building the module root.
- `make build`, `make live-*`, `make compare`, `make modrun` and
  `make modbench` change their paths. A deployment that runs
  `ganglion --roles …` has to add `run`.
- `--help` lists every setting with its variable and default. That becomes
  the reference, alongside `docs/README.md`.
- The YAML node config of D-3 is not read through flags. If it ever is
  (`cli-altsrc`), that is a separate decision.

## Revisit when

- The node config of D-3 needs to override flags, or flags need to come
  from the YAML: then the source chain is designed together with D-3.
- urfave/cli v3 changes its flag or source API in a breaking way.
- A second shipped binary appears: then L2's `cmd/<binary>` layout is
  worth another look.
