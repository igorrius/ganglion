# ADR-0059: Make `ganglion.yaml` the node's single source of configuration, with secrets as `env:NAME`

- **Status:** Proposed
- **Date:** 2026-11-09
- **Deciders:** Igor Artasevych
- **Spec:** [7.14](../spec.md#714-node-configuration) (R-CFG-1…7), [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-6, R-RUN-8), [7.9](../spec.md#79-observability) (R-OBS-2, R-OBS-3); [M4 design](../plans/p1-m4-reflexes-design.md) section 4; decision D-3 of the [P1 phase plan](../plans/p1-all-in-one.md); [ADR-0012](0012-yaml-manifests.md), [ADR-0023](0023-json-schemas-for-yaml-files.md), [ADR-0029](0029-urfave-cli-for-commands-and-bootstrap-flags.md), [ADR-0056](0056-small-hosts-as-a-soft-target.md)

## Context

D-3 of the phase plan was left for M4: how the node is configured and
where secrets come from. Its requirements:
- bootstrap (data directory, roles) from flags or the environment;
- the node config as YAML that can live in a git repository, which the
  agent changes only with approval (M6);
- secrets never in the YAML, read through one secret source (the
  environment and `*_FILE` files today, package `secrets`);
- the same binary on Railway, in Docker or Kubernetes, and as a local
  install.

What exists today:
- about 25 `GANGLION_*` variables, each with a flag (ADR-0029): time zone,
  limits, log level and folder, metrics address, reactions, rich messages,
  reply mode, search, sandbox, workspace, admin and more;
- a separate model catalogue, `models.yaml`, found through
  `GANGLION_MODELS_FILE`, whose providers name their key with
  `key_env: NAME`;
- module manifests in YAML (ADR-0012), but no place that says which
  modules a node runs and with what config.

M4 adds modules installed at run time, with their config, and the
operator's memory limit (R-RUN-6). On Railway a variable left over from
an old deploy, or misspelt, leaves the node on a default without a word.

Four questions follow: what goes in the file, whether the environment
can override it, how a secret field is written, and when a change
applies.

## Options

### A. Only the new M4 things in YAML

The file holds modules (and, as D-3 had it, cells and chats). Every
existing variable stays where it is.

### B. Everything an operator chooses in YAML; the file alone

Every setting the operator chooses moves into the file, models included.
The environment keeps only what is needed to find the file and start: the
config path, the database path, the roles, and the secrets.

### C. As B, with the environment overriding the file

The file is the base; a `GANGLION_*` variable of the same meaning wins
over it, as in many twelve-factor programs.

### Secret fields

- **`env:NAME` in the field itself:** `token: env:TELEGRAM_BOT_TOKEN`.
  The node reads `NAME` or the file `NAME_FILE`.
- **`key_env: NAME` beside the field,** as `models.yaml` does today.

### When a change applies

- **Modules live, the rest at restart.**
- **Everything live:** every section reloads on change.
- **Everything at restart.**

## Comparison

| Criterion | A. M4 things only | B. File only | C. File, env overrides |
|---|---|---|---|
| Where an operator looks for a setting | Two places, by age of the feature | One file | The file, then every variable |
| A stale or misspelt variable | Silently used or ignored | Fails the start with a hint | Silently wins |
| The agent changes config with approval (M6) | Only part of it | All of it, as a diff of one file | A diff that a variable can undo |
| One schema documents every field (ADR-0023) | No | Yes | Yes, plus a variable table |
| Editing on Railway | Variables in the UI | A file on the volume | Either, which is the problem |
| Effort in M4 | Least | Move every setting | Move every setting and keep both paths |

| Criterion | `env:NAME` | `key_env: NAME` |
|---|---|---|
| One rule for every secret field | Yes | A sibling field for each secret |
| A literal pasted by mistake | Detectable: anything not `env:` is refused | The literal goes in the real field unnoticed |
| Room for another source later | `env:` is a scheme; another can be added | A new sibling per source |

| Criterion | Modules live, rest at restart | All live | All at restart |
|---|---|---|---|
| R-RUN-8 (install without restart) | Kept | Kept | Broken |
| Code to reload each section safely | Modules only, built anyway | Every component | None |
| A restart loses nothing (M2) | Yes | — | Yes |

## Decision

**B, the file alone, with `env:NAME` and modules applied live** (owner,
2026-11-09). One file the operator can read, the agent can diff and the
schema can document; a stale variable stops the node instead of steering
it.

- **Outside the file:** the config path (`--config` or `GANGLION_CONFIG`,
  default `ganglion.yaml` beside the database), the database path (`--db`
  or `GANGLION_DB`), the roles (the `--roles` flag), and secrets. No file
  at the default path means the defaults, as a node without variables
  behaves today; a path given with `--config` or `GANGLION_CONFIG` must
  exist, so a typo in it does not start a node on defaults.
- **Secrets are references.** A secret field holds `env:NAME`; the node
  reads `NAME` or `NAME_FILE`. A literal in a secret field fails the
  start. Module secrets are not in the file: the manifest names them and
  the node reads them from the same source.
- **Old variables fail the start** with a hint naming the new key
  (`GANGLION_TZ is no longer read: set node.tz in ganglion.yaml`); so do
  `GANGLION_MODELS_FILE`, and `key_env` or `tier` pasted from the old
  models file into the `models:` section. The variables that stay are
  `GANGLION_CONFIG`, `GANGLION_DB`, `OTEL_EXPORTER_OTLP_ENDPOINT`, the MCP
  bridge's per-run `GANGLION_MCP_TOKEN` and the test tools' variables
  (`GANGLION_LIVE_*`, `GANGLION_ACCEPT_OUT`, `GANGLION_MEASURE*`,
  `GANGLION_CRASH_*`); every other `GANGLION_*` that held a setting before
  M4 fails the start. `models.yaml` becomes the file's
  `models:` section ([ADR-0060](0060-models-by-purpose.md)).
- **Strict parsing:** an unknown key fails the start; errors name the path
  and the reason (`ganglion.yaml: agent.limits.daily_turns: must be 0 or
  more`).
- **`off` turns a thing off; a missing key means the default** (`limits:
  off`, `limits: {daily_turns: off}`, `metrics.addr: off`).
- **A schema with descriptions:** `schemas/node.schema.json` gives every
  field a description, a type, a default and the allowed values; a test
  keeps it in step with the loader (ADR-0023);
  `examples/ganglion.example.yaml` comments every field.
- **A `modules:` line holds the name, the module's config and `off`.**
  Which version runs, and its files, stay in the module store of ADR-0041
  (`<data>/modules/<name>/<sha256>.wasm` and `module_versions`), so the
  file never names a path the store owns.
- **The node edits its own file carefully.** `module.install` and
  `/modules config|off|on|remove` change only their module's line,
  through the YAML node tree, so the operator's comments and order stay.
  They write a temporary file and rename it over the old one, so a crash
  leaves either the old file or the new one.
- **When it applies:** modules (install, update, rollback, config, off and
  on, remove) at once; every other section at the next start. `/restart`
  (admin) ends the node cleanly and Railway or systemd starts it again.
- **`node.memory_limit`** sets `GOMEMLIMIT`; without it, about 80 % of the
  container's limit (R-RUN-6, ADR-0056). A `GOMEMLIMIT` the operator set
  wins over both: it is the Go runtime's own setting, like `OTEL_*`, not a
  retired ganglion variable.
- **One exception:** `OTEL_EXPORTER_OTLP_ENDPOINT` stays an environment
  variable. It is OpenTelemetry's own setting, not ganglion's, and gets no
  key until the owner has a collector.

## Security

- **Trust boundaries:** the file becomes one. What it says the node runs,
  including which modules and with what rights. Its writers are the
  operator, `module.install` (the `modules:` lines, after the owner's
  press), and from M6 the agent with approval.
- **What an attacker controls:** a stale or planted environment variable,
  the file once it is a git repository (M6), and the text of a module's
  config block.
- **Abuse cases and what stops them:**
  - *A token pasted into the file reaches git.* A secret field accepts only
    `env:NAME`; a literal fails the start.
  - *A forgotten Railway variable silently changes behaviour.* Every old
    `GANGLION_*` name is a list in code; finding one fails the start.
  - *A misspelt key silently leaves a default in force* (a limit that is
    not applied). Strict parsing refuses unknown keys.
  - *`env:NAME` used to send one secret to another service* (`brave_key:
    env:TELEGRAM_BOT_TOKEN`). Only the operator and the approved paths
    write the file; the mapping is visible in it and, from M6, in its
    diff. Nothing in code stops an operator from pointing a field at the
    wrong variable.
  - *A secret value in a start error.* Errors name the path and the
    variable, never the value.

## Consequences

- D-3 closes. Cells leave its list: they are declared in module manifests
  ([ADR-0062](0062-cells-owned-by-modules.md)). Chats and topics are not
  in M4: they served load-shedding notices ([ADR-0058](0058-load-shedding-leaves-p1.md)).
- Settings recorded in accepted ADRs as variables or flags become keys:
  `--log-dir` (ADR-0043) is `log.dir`, `GANGLION_SANDBOX_HIDE_NAMES`
  (ADR-0057) is `sandbox.hide_names`, the metrics address (ADR-0026) is
  `metrics.addr`. The flags of ADR-0029 shrink to the bootstrap ones.
  Those ADRs are not rewritten; this one records the move.
- On Railway the operator edits a file on the volume (over `railway ssh`)
  instead of variables in the UI. The volume, not the image, holds the
  file, so the agent can change it in M6.
- Changing anything but modules needs `/restart`. Delivery loses nothing
  across it (M2).
- A container sized for builds (about 1 GB) must set `node.memory_limit`,
  or the heap would be allowed about 800 MB.
- The lab, `make accept` and the scripts move to example files.
- The list of old variables is code; the bridge's token and the test
  tools' variables are not node configuration and are not on it.
- The file and the module store share one fact, the module's name; a
  version change touches only the store, never the file.

## Revisit when

- M6 puts the file in git and the agent edits it: review who may write
  which section.
- A second secret source arrives (an encrypted store): `env:` gets a
  sibling scheme.
- A section other than modules needs to change without a restart.
