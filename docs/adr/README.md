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
| [0023](0023-json-schemas-for-yaml-files.md) | Publish JSON Schemas for ganglion's YAML files, kept in sync with the Go loaders by tests | Accepted | 2026-11-02 |
| [0024](0024-persona-as-workspace-files.md) | Keep the agent's persona in Markdown files of the workspace; the core keeps only its rules in code | Accepted | 2026-11-02 |
| [0025](0025-markdown-rendered-to-telegram-entities.md) | Agents write Markdown; the Telegram transport renders it to message entities with goldmark | Accepted | 2026-11-02 |
| [0026](0026-opentelemetry-prometheus-and-promtop.md) | Instrument with OpenTelemetry; serve metrics to Prometheus with the promtop dashboard, export traces over OTLP | Accepted | 2026-11-02 |
| [0027](0027-written-go-conventions-and-lint-guardrails.md) | Write the Go conventions down and enforce what a linter can | Accepted | 2026-11-02 |
| [0028](0028-samber-do-composition-root-in-config.md) | Assemble the node in a `config` package of `samber/do` providers | Accepted | 2026-11-02 |
| [0029](0029-urfave-cli-for-commands-and-bootstrap-flags.md) | Use urfave/cli v3 for commands and bootstrap flags, with `main.go` at the root and the command tree in `cmd` | Accepted | 2026-11-02 |
| [0030](0030-mustinvoke-inside-providers.md) | Take dependencies with do.MustInvoke inside providers (supersedes the `Must*` rule of ADR-0028) | Accepted | 2026-11-02 |
| [0031](0031-agent-security-baseline.md) | Adopt a security baseline for the agent: rules in code, session-wide taint, a clean-context check and grants for scheduled tasks | Proposed | 2026-11-02 |
| [0032](0032-buttons-and-resume.md) | Answer confirmations with one-time buttons and resume the conversation with a new event | Proposed | 2026-11-03 |
| [0033](0033-memory-in-the-workspace-git-with-the-git-binary.md) | Keep memory as Markdown files in the workspace's git repository, driven by the `git` binary (D-7) | Proposed | 2026-11-03 |
| [0034](0034-clean-context-guard.md) | Check every memory write with a cheap model in a clean context, failing closed | Proposed | 2026-11-03 |
| [0035](0035-web-reading.md) | Read pages through an html2md WASM module in Rust; search with Brave or DuckDuckGo | Proposed | 2026-11-03 |
| [0036](0036-mcp-bridge-over-a-per-run-unix-socket.md) | Give CLI providers the agent's tools over MCP on a per-run Unix socket with a token | Proposed | 2026-11-03 |
| [0037](0037-own-minimal-mcp-server.md) | Write a minimal MCP server of our own instead of using the official Go SDK | Proposed | 2026-11-03 |
| [0038](0038-host-commands-as-argv-with-classes-in-code.md) | Give the agent host commands as argv, classed by a table in code, in a milestone M3d | Proposed | 2026-11-03 |
| [0039](0039-command-sandbox.md) | Run host commands and skill scripts in a sandbox that fails closed | Proposed | 2026-11-03 |
| [0040](0040-plans-as-approvals.md) | Let one press on a plan approve exactly the commands it lists | Proposed | 2026-11-03 |

Open questions that will become ADRs are listed in
[spec section 11](../spec.md#11-open-decisions).

## Writing a new ADR

1. Copy [template.md](template.md) to `NNNN-short-title.md` with the next
   number.
2. Fill in the context and options first; compare before deciding.
3. Add a row to the table above.
4. Accepted ADRs are not rewritten. To change a decision, write a new ADR
   and mark the old one "Superseded by ADR-NNNN".
