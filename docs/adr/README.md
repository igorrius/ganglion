# Architecture decision records

Why the system is the way it is. Each record states the context, the options
considered, how they compare, the decision and its consequences. See
[ADR-0001](0001-record-architecture-decisions.md) for when an ADR is needed.

| # | Decision | Status | Date |
|---|---|---|---|
| [0001](0001-record-architecture-decisions.md) | Record architecture decisions as ADRs | Accepted | 2026-11-01 |
| [0002](0002-docs-as-markdown-and-mermaid.md) | Keep docs as Markdown and Mermaid in the repo, readable in Obsidian | Accepted | 2026-11-01 |
| [0003](0003-build-from-scratch.md) | Build from scratch; treat existing agents as a source of ideas | Accepted | 2026-10-31 |
| [0004](0004-one-binary-with-roles.md) | One binary with roles; one process per node | Accepted | 2026-10-31 |
| [0005](0005-wasm-on-wazero-for-modules.md) | Run compiled modules as WASM on wazero | Accepted | 2026-10-31 |
| [0006](0006-sqlite-inside-nats-between-nodes.md) | SQLite inbox/outbox inside a node, NATS JetStream between nodes | Accepted | 2026-10-31 |
| [0007](0007-openai-compatible-first-provider.md) | Start with an OpenAI-compatible provider client | Accepted | 2026-10-31 |
| [0008](0008-agent-never-rebuilds-core.md) | The agent extends the system but never rebuilds the core | Accepted | 2026-10-31 |
| [0009](0009-claude-subscription-only-via-cli-worker.md) | Use a Claude subscription only through an unmodified claude CLI worker | Accepted; conversation rule superseded by 0021 | 2026-10-31 |
| [0010](0010-documentation-in-english.md) | Write all documentation in English | Accepted | 2026-11-01 |
| [0011](0011-module-abi.md) | Module ABI — own host interface or Extism | Accepted: own ABI | 2026-11-01 |
| [0012](0012-yaml-manifests.md) | Write module manifests in YAML, parsed strictly with go.yaml.in/yaml/v3 | Accepted | 2026-11-01 |
| [0013](0013-protocol-envelope-around-stateless-mcp.md) | Core ↔ edge protocol is our envelope over NATS carrying stateless MCP; A2A only for external agents | Accepted | 2026-11-01 |
| [0014](0014-state-cells.md) | Flows act on state cells with swappable backends and observers | Accepted | 2026-11-01 |
| [0015](0015-agent-first-p1.md) | Build the agent early in P1 and let it reconfigure the system | Accepted | 2026-11-01 |
| [0016](0016-sqlite-migrations.md) | Apply SQLite migrations with a small built-in migrator, per component | Accepted | 2026-11-01 |
| [0017](0017-extension-points.md) | Map the extension points: the core keeps guarantees and trust, everything else is contributed | Accepted | 2026-11-02 |
| [0018](0018-own-telegram-bot-api-client.md) | Use our own minimal Telegram Bot API client | Accepted | 2026-11-02 |
| [0019](0019-release-leases-at-core-start.md) | Release inbox leases when a core starts; one core process owns a database file | Accepted | 2026-11-02 |
| [0020](0020-neutral-history-as-conversation-state.md) | Keep history in our own neutral format, stored as the conversation state | Accepted | 2026-11-02 |
| [0021](0021-providers-chosen-by-the-operator-claude-cli-included.md) | Let the operator choose one or more providers, the claude CLI included, for conversation | Accepted | 2026-11-02 |
| [0022](0022-haiku-first-during-development.md) | During development, Haiku through the claude CLI answers first and DeepSeek V4.1 Flash is the fallback | Accepted | 2026-11-02 |

Open questions that will become ADRs are listed in
[spec section 11](../spec.md#11-open-decisions).

## Writing a new ADR

1. Copy [template.md](template.md) to `NNNN-short-title.md` with the next
   number.
2. Fill in the context and options first; compare before deciding.
3. Add a row to the table above.
4. Accepted ADRs are not rewritten. To change a decision, write a new ADR
   and mark the old one "Superseded by ADR-NNNN".
