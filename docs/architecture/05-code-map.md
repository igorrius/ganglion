# Level 5 — Code map

Where each part of the architecture lives in the repository, and what
exists today. Update this table in the same commit that adds, moves or
implements a package.

Status: **done** — implemented and tested; **interface** — types and
interfaces only; **planned** — not in the repository yet.

| Concept | Package | Status | Spec | ADR |
|---|---|---|---|---|
| Binary: `main.go` (signals) at the root; the command tree (`run`, `version`) in `cmd`, the flags with their environment variables in `cmd/flags`; the kill -9 crash harness and the memory test in `cmd` | `.` (main), `cmd`, `cmd/flags` | done (P0); crash harness (P1 M2); urfave/cli (P1 refactor) | 5.1 | [0004](../adr/0004-one-binary-with-roles.md), [0029](../adr/0029-urfave-cli-for-commands-and-bootstrap-flags.md) |
| Build information: version, commit, toolchain of this binary | `internal/buildinfo` | done (P1 refactor) | — | [0029](../adr/0029-urfave-cli-for-commands-and-bootstrap-flags.md) |
| Composition root, dependency logic only: one `samber/do` provider per component; extension points (handler, give-up, turn hook, transports, receivers, housekeeping) as an interface provider that selects and one named provider per implementation, no-ops included; a `do.Package` per role, `Run`; `Bootstrap`, filled by the command line | `config` | done (P1 refactor) | 5.1 | [0028](../adr/0028-samber-do-composition-root-in-config.md) |
| Role runner: `Component`, `RunAll`, `Run` by roles; the database lock; Telegram polling as a component with the 409 alert | `node` | done; edge and worker are stubs | 5.1 | [0004](../adr/0004-one-binary-with-roles.md), [0019](../adr/0019-release-leases-at-core-start.md) |
| `Event`, `Outgoing`, TTL expiry, ULID ids, the neutral `Message` payload | `event` | done | 6, 9.1 | — |
| Transport interface, send errors (`ErrPermanent`, `RetryAfterError`), `Parter` | `transport` | interface | 6, 7.1 | [0017](../adr/0017-extension-points.md) |
| Inbox / outbox: `Queue`, `Outbox` | `queue` | interface | 7.1 | [0006](../adr/0006-sqlite-inside-nats-between-nodes.md) |
| Model providers: neutral messages, MCP tools, error kinds | `provider` | interface | 7.4 | [0007](../adr/0007-openai-compatible-first-provider.md), [0020](../adr/0020-neutral-history-as-conversation-state.md) |
| OpenAI-compatible provider (OpenRouter, Ollama, …); fake API for tests | `provider/openai`, `provider/providertest` | done (P1 M3a) | 7.4 | [0007](../adr/0007-openai-compatible-first-provider.md) |
| claude CLI as a conversation provider (`claude -p`, history on stdin) | `provider/claudecli` | done (P1 M3a) | 7.4 | [0021](../adr/0021-providers-chosen-by-the-operator-claude-cli-included.md) |
| Model router: catalogue types, `Route`, `Report` | `router` | interface | 7.3 | — |
| Rules router and YAML model catalogue: filters, tiers, budget, cooldown | `router/rules` | done (P1 M3a) | 7.3 | [0021](../adr/0021-providers-chosen-by-the-operator-claude-cli-included.md), [0022](../adr/0022-haiku-first-during-development.md) |
| Turn trace: route, tools, tokens, cache, cost; daily totals | `turns` | done (P1 M3a) | 7.2 | — |
| History (as conversation state), cron_runs | history in `agent`; cron_runs planned (P1) | history done (P1 M3a) | 6 | [0020](../adr/0020-neutral-history-as-conversation-state.md) |
| Access list: `allowed` table with roles; sink filter that drops strangers | `access` | done (P1 M2) | 7.8 | — |
| SQLite queue: inbox (ordered leases, atomic `Complete`), outbox (heads per conversation, retries, parts), conversation state, prune and the daily `Pruner` component | `queue/sqlqueue` | done (P1 M2) | 7.1 | [0006](../adr/0006-sqlite-inside-nats-between-nodes.md) |
| Outbox sender: order per conversation, backoff, `retry_after` pause, resumable parts | `outbox` | done (P1 M2) | 7.1 | — |
| Telegram transport: own Bot API client, long polling (offset after the sink), conversation keys, Markdown rendered to entities (goldmark), block-aware split, replies (auto/always/off), typing; fake Bot API for tests | `transport/telegram`, `transport/telegram/telegramtest` | done (P1 M2) | 7.1 | [0018](../adr/0018-own-telegram-bot-api-client.md) |
| Turn loop: parallel workers, lease → handler → atomic complete, turn timeout, give up after max attempts; echo handler | `core` | done (P1 M2, workers M3a); the agent is the handler when a catalogue is set | 7.1, 7.2 | [0019](../adr/0019-release-leases-at-core-start.md) |
| Agent: a turn is a pipeline of stages (idle session, commands, daily limits, answer, compaction); model calls with fallback; tools run through `tool.Middleware` (tracing, then the journal) with a numbered `tool.Call` per call; an external result taints the session and reaches the model wrapped as data; the known-URL set for `web.fetch`; tools only for admins (`AdminFunc`); a command table (`/new`, `/sessions`, `/resume`); sessions archive (`SessionStore`, `NoArchive`); core rules + persona from `workspace/persona/*.md`; conversation state v2 (adds `Tainted` and `Pending`, reads v1; compaction keeps the taint and stores the summary wrapped) | `agent` | done (P1 M3a); tool context, taint and state v2 (P1 M3b task 2); memory, depth limit, first-run introduction later in M3b | 7.2 | [0020](../adr/0020-neutral-history-as-conversation-state.md), [0024](../adr/0024-persona-as-workspace-files.md), [0031](../adr/0031-agent-security-baseline.md) |
| Tools: `Spec`, `Call`, `Result` (text, external flag, pending id, outgoing messages), `Handler`, `Registry` (admin-only tools hidden from others and refused at run), `Middleware`, `Grant`, `WrapExternal` | `tool` | done (P1 M3b task 2); the tools themselves in later tasks | 7.6 | [0031](../adr/0031-agent-security-baseline.md) |
| Tool-call journal: one row per decision on a call, arguments and results redacted and capped, daily prune component | `tool/journal` | done (P1 M3b task 2) | 7.6 | [0031](../adr/0031-agent-security-baseline.md) |
| Web address normalisation for the known-URL check | `internal/urlnorm` | done (P1 M3b task 2) | 7.6 | [0031](../adr/0031-agent-security-baseline.md) |
| Compare models on scripted conversations (D-6 tool) | `tools/modelcompare` | done (P1 M3a) | 7.4 | [0022](../adr/0022-haiku-first-during-development.md) |
| Telemetry: OpenTelemetry setup, Prometheus `/metrics`, the promtop dashboard, OTLP traces, trace ids in logs; in-memory providers for tests | `telemetry`, `telemetry/telemetrytest` | done (P1) | 7.9 | [0026](../adr/0026-opentelemetry-prometheus-and-promtop.md) |
| JSON Schemas of the YAML files (model catalogue, module manifest), embedded | `schemas` | done (P1 M3a); node config schema with D-3 in M4 | 7.3, 7.5 | [0023](../adr/0023-json-schemas-for-yaml-files.md) |
| Alerts to every admin's private chat (daily limit, give-up, 409) | `alert` | done (P1 refactor) | 7.8 | — |
| Secret source: the environment and `*_FILE` files; an encrypted store later | `secrets` | done (P1 refactor) | D-3 | [0029](../adr/0029-urfave-cli-for-commands-and-bootstrap-flags.md) |
| Shared helpers, not library API: backoff with jitter, rune truncation, plain transcript, start of day, URL normalisation | `internal/backoff`, `internal/text`, `internal/transcript`, `internal/clock`, `internal/urlnorm` | done (P1 refactor) | — | [0027](../adr/0027-written-go-conventions-and-lint-guardrails.md) |
| Policies | — | planned (P1) | 7.6 | — |
| Memory | — | planned (P1) | 7.7 | — |
| Runner: wazero host, manifest, limits | `runner` | done (P0 spike, own ABI); supervisor and scheduler planned (P1) | 7.5 | [0005](../adr/0005-wasm-on-wazero-for-modules.md), [0011](../adr/0011-module-abi.md), [0012](../adr/0012-yaml-manifests.md) |
| Module-side kit: `Host`, wire types | `pdk` | done (P0) | 7.5 | [0011](../adr/0011-module-abi.md) |
| Module-side ABI glue (Go, TinyGo) | `pdk/guest` | done (P0) | 7.5 | [0011](../adr/0011-module-abi.md) |
| Host conformance suite and probe module | `runner/runnertest`, `runner/runnertest/probe` | done | 7.5 | [0011](../adr/0011-module-abi.md) |
| Deye reader module | `modules/deye` (logic), `modules/deye/wasm` (main), `modules/deye/deye-reader.yaml` | done; matches `deye.py` on the live API | 8.3 |
| Deye reader in Rust (toolchain comparison) | `modules/deye-rs` (cargo; not built in CI) | done | 8.3 |
| Measure modules (size, load, latency, RSS) | `tools/modbench` | done (P0) | 10.1 | [0005](../adr/0005-wasm-on-wazero-for-modules.md) |
| Run a module once (dev and acceptance tool) | `tools/modrun` | done (P0) | 10.1 | — |
| State cells: switch, sensor, request; service, watch, reconcile | `cells` | done (P1 M1) | 8 | [0014](../adr/0014-state-cells.md) |
| Cell stores | `cells` (`MemStore`), `cells/sqlstore`; suite `cells/storetest` | done (P1 M1) | 8 | [0014](../adr/0014-state-cells.md) |
| Cell backends | `cells/virtual` (fault injection); suite `cells/cellstest` | done (P1 M1); `tuya` later | 8 | [0014](../adr/0014-state-cells.md) |
| SQLite: open, per-component migrations | `sqlitedb` | done (P1 M1) | 7.1 | [0016](../adr/0016-sqlite-migrations.md) |
| Load-shedding flow | — | planned (P1) | 8 | [0014](../adr/0014-state-cells.md) |
| Core ↔ edge protocol, NATS | — | planned (P2) | 9.1 | [0006](../adr/0006-sqlite-inside-nats-between-nodes.md) |

## Dependency rules

```mermaid
flowchart BT
    event["event"]
    transport["transport"] --> event
    queue["queue"] --> event
    provider["provider"]
    router["router"]
    openaip["provider/openai"] --> provider
    claudecli["provider/claudecli"] --> provider
    rulesp["router/rules"] --> router
    rulesp --> provider
    turnsp["turns"] --> sqlitedb
    agentp["agent"] --> provider
    agentp --> router
    agentp --> turnsp
    agentp --> toolp["tool"]
    toolp --> event
    journalp["tool/journal"] --> toolp
    journalp --> sqlitedb
    configp --> journalp
    agentp --> queue
    configp --> agentp
    configp --> rulesp
    configp --> openaip
    configp --> claudecli
    node["node"]
    configp["config"] --> node
    cmd["cmd"] --> configp
    mainp["main"] --> cmd
    cmd --> node
    cmd --> telemetryp["telemetry"]
    corep["core"] --> queue
    corep --> event
    tgtest["transport/telegram/telegramtest"]
    tgp -. tests .-> tgtest
    cmd -. tests .-> tgtest
    configp --> corep
    configp --> sqlqueue
    configp --> outboxp
    configp --> accessp
    configp --> tgp["transport/telegram"]
    node --> tgp
    node --> alertp
    tgp --> transport
    pdk["pdk"]
    deye["modules/deye"] --> pdk
    runner["runner"] --> pdk
    modrun["tools/modrun"] --> runner
    modbench["tools/modbench"] --> runner
    guest["pdk/guest"] --> pdk
    cellsp["cells"]
    sqlstore["cells/sqlstore"] --> cellsp
    sqlstore --> sqlitedb["sqlitedb"]
    virtualb["cells/virtual"] --> cellsp
    sqlqueue["queue/sqlqueue"] --> queue
    sqlqueue --> sqlitedb
    outboxp["outbox"] --> queue
    outboxp --> transport
    accessp["access"] --> transport
    accessp --> sqlitedb
    alertp["alert"] --> event
    agentp --> alertp
    configp --> alertp
    cmd --> secretsp["secrets"]
```

- `event` and `pdk` are leaves: they import nothing from the project.
- `internal/*` packages are helpers shared by several packages; they are
  not part of the library API and are left out of the diagram.
- Module code (`modules/*`) depends only on `pdk`, never on `runner`, so it
  compiles to WASM.
- Interface packages (`transport`, `queue`, `provider`, `router`) depend only
  on `event` and the standard library, so a node can be embedded as a
  library (spec principle 5).
- Implementations go into their own packages and depend on the interfaces,
  never the other way round.
- Only `config` wires concrete implementations together (ADR-0028); it is
  the only package that imports `samber/do`.
