# ganglion docs

Start here when coming back to the project.

## Where we are

- **Phase:** P0 · Recon is complete; P1 · All-in-one is next
  ([plan](spec.md#10-plan-phases-and-gates)).
- **P0 results:**
  - 0.1 repository skeleton: `cmd/ganglion`, interface packages, CI.
  - 0.2 Deye reader in WASM on wazero: matched `deye.py` on the live API,
    12 of 12 fields.
  - 0.3 module ABI and toolchains: own ABI
    ([ADR-0011](adr/0011-module-abi.md)); Rust and TinyGo modules are 5–20×
    smaller than standard Go.
  - 0.4 MCP and A2A: our envelope over NATS carrying stateless MCP
    ([ADR-0013](adr/0013-protocol-envelope-around-stateless-mcp.md),
    [research](research/2026-11-01-mcp-a2a-coverage.md)).
  - 0.5 runner memory: fits a 128 MB container with room to spare
    ([research](research/2026-11-01-runner-memory.md)); a Pi run later
    confirms arm64 timings.
  - 0.6 Tuya access: moved to P1; loads are state cells with a `virtual`
    backend until the plugs are linked
    ([ADR-0014](adr/0014-state-cells.md)).
  - 0.7 home internet outlasts the inverter; DeyeCloud quota is ample
    ([research](research/2026-11-01-outage-connectivity-and-deye-limits.md)).
- **P1 progress:**
  - M1 state cells done — `cells`, `cells/sqlstore`, `cells/virtual`,
    `sqlitedb` ([plan](plans/p1-m1-cells.md)).
  - M2 delivery and Telegram done — `queue/sqlqueue`, `outbox`,
    `transport/telegram`, `access`, `core`, the core role in `node`
    ([plan](plans/p1-m2-delivery-telegram.md)). An echo bot that survives
    500 `kill -9` without losing a message; 21 MB RSS; run live on the
    test bot. Decisions: [ADR-0018](adr/0018-own-telegram-bot-api-client.md)
    (own Bot API client), [ADR-0019](adr/0019-release-leases-at-core-start.md)
    (leases released at core start, database lock).
  - M3a, the talking agent, built — `provider/openai`, `provider/claudecli`,
    `router/rules`, `turns`, `agent`, core workers
    ([plan](plans/p1-m3a-talking-agent.md)). The bot answers through
    models the operator lists in a catalogue: Haiku through the claude CLI
    first, DeepSeek V4.1 Flash on OpenRouter as the fallback. Sessions
    (`/new`, idle reset, `/sessions`, `/resume`), daily limits, a
    turns trace; 24 MB RSS. Decisions: [ADR-0020](adr/0020-neutral-history-as-conversation-state.md)
    (history as conversation state), [ADR-0021](adr/0021-providers-chosen-by-the-operator-claude-cli-included.md)
    (operator-chosen providers, CLI included), [ADR-0022](adr/0022-haiku-first-during-development.md)
    (Haiku first during development), [ADR-0023](adr/0023-json-schemas-for-yaml-files.md)
    (JSON Schemas for YAML files), [ADR-0024](adr/0024-persona-as-workspace-files.md)
    (persona in workspace files), [ADR-0025](adr/0025-markdown-rendered-to-telegram-entities.md)
    (Markdown rendered to Telegram entities; typing; replies). Run live
    on the test bot by the owner; formatting checked after task 11.
    Closed; the work after the M3a review (persona, schemas, formatting,
    observability) has had no independent review yet.
  - Observability ([plan](plans/p1-observability.md),
    [ADR-0026](adr/0026-opentelemetry-prometheus-and-promtop.md)):
    - one info log line per turn;
    - OpenTelemetry metrics at `http://127.0.0.1:9464/metrics` with the
      promtop dashboard at `/webmetrics/`;
    - OTLP traces when `OTEL_EXPORTER_OTLP_ENDPOINT` is set;
    - Go 1.27.3;
    - the node is 33 MB on disk and 35 MB RSS after 1,000 turns.
- **Next:** a refactoring before M3b ([plan](plans/p1-refactor.md)):
  - the [Go conventions](conventions.md) and lint guardrails
    ([ADR-0027](adr/0027-written-go-conventions-and-lint-guardrails.md));
  - the agent as a stage pipeline;
  - the node assembled in `config` with `samber/do`
    ([ADR-0028](adr/0028-samber-do-composition-root-in-config.md));
  - `ganglion run` on urfave/cli
    ([ADR-0029](adr/0029-urfave-cli-for-commands-and-bootstrap-flags.md)).

  No behaviour changes.
- **Then:** M3b — memory, tools and policies, skills, and the 3.8
  acceptance under the crash harness.
  - Scope stays one operator, one agent, one bot (spec 14).
  - Carried into M3b: a first-run "introduction" conversation that
    writes the persona ([ADR-0024](adr/0024-persona-as-workspace-files.md));
    the current time in the last user message (the model
    does not know the date); tools for the claude CLI through MCP
    config; the depth limit (R-AGT-4) and tool placement (R-RTR-3).
  - Deferred minors from the reviews:
    - M2: record a send's outcome with `context.WithoutCancel` on
      shutdown; keep the seeded admin's name and plan admin removal; do
      not end the node when the `409` alert cannot be stored.
    - M3a: move to goldmark v2 (`github.com/yuin/goldmark/v2`), where
      upstream now lands some fixes first; remove or document `provider.Fallback`; clean up the claude
      work dir; name people, not ids, in limit alerts.
- **Open decisions:** [spec section 11](spec.md#11-open-decisions)

Update this block when a task or phase closes.

## Owner setup for live checks

What the owner sets up by hand before the first live run on the test bot
(M2 task 8 of the [M2 plan](plans/p1-m2-delivery-telegram.md)). Nothing here
is needed for tests: they run against a fake Bot API.

- **Test bot:** @ganglion_test_bot, used only by ganglion (since
  2026-11-02; the development bot has another project's webhook). Its
  token is `TELEGRAM_TEST_BOT_TOKEN`. Two runs at once get `409 Conflict`,
  and the older one stops polling (R-DLV-8).
- **Your Telegram user id:** needed for the admin entry in the access list
  (`GANGLION_ADMIN=telegram:user:<id>`); strangers get no reply. Any
  "user info" bot shows it, or the `from.id` of your message in
  `getUpdates`.
- **Running locally:** a `.env` file in the repository root (git-ignored):

  ```sh
  TELEGRAM_TEST_BOT_TOKEN=<token from @BotFather>
  GANGLION_ADMIN=telegram:user:<your id>
  ```

- **Running in a Claude Code cloud session:** a local `.env` does not reach
  the container. In the cloud environment's settings (the environment menu
  in the session title bar → Edit):
  1. Network access: add `api.telegram.org` to Allowed domains, keeping
     "Allow package managers" ticked
     ([docs](https://code.claude.com/docs/en/cloud-environments#network-access)).
  2. Add `TELEGRAM_TEST_BOT_TOKEN` and `GANGLION_ADMIN` as secrets or
     environment variables. A new session picks them up. Never paste the
     token into the chat.

## Map

| Question | Read |
|---|---|
| What are we building, what is in scope now, what are the requirements? | [spec.md](spec.md) |
| What does the system look like, from the big picture down to packages? | [architecture/](architecture/README.md) |
| Why was it decided this way, and what else was considered? | [adr/](adr/README.md) |
| Where is X in the code, and does it exist yet? | [architecture/05-code-map.md](architecture/05-code-map.md) |
| What did we find out before deciding? | [research/](research/) |
| How do we write Go code here? | [conventions.md](conventions.md) |
| What is the plan for the current phase? | [plans/](plans/) |

Suggested path after a long break: this page → the
[one-picture overview](architecture/README.md#the-idea-in-one-picture) →
[containers](architecture/02-containers.md) → the current phase in the
[spec](spec.md#10-plan-phases-and-gates) → the [code map](architecture/05-code-map.md).

## Reading in Obsidian

The `docs/` folder works as an Obsidian vault: graph view, backlinks and
quick switching come for free, and Mermaid diagrams render natively.

1. Open `docs/` as a vault ("Open folder as vault").
2. Settings → Files and links: turn off **Use [[Wikilinks]]** and set
   **New link format** to **Relative path to file**, so new links keep
   working on GitHub ([ADR-0002](adr/0002-docs-as-markdown-and-mermaid.md)).

`docs/.obsidian/` is git-ignored, so vault settings stay local.
