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
| [0022](0022-haiku-first-during-development.md) | During development, Haiku through the claude CLI answers first and DeepSeek V4.1 Flash is the fallback | Superseded by 0047 | 2026-11-02 |
| [0023](0023-json-schemas-for-yaml-files.md) | Publish JSON Schemas for ganglion's YAML files, kept in sync with the Go loaders by tests | Accepted | 2026-11-02 |
| [0024](0024-persona-as-workspace-files.md) | Keep the agent's persona in Markdown files of the workspace; the core keeps only its rules in code | Accepted | 2026-11-02 |
| [0025](0025-markdown-rendered-to-telegram-entities.md) | Agents write Markdown; the Telegram transport renders it to message entities with goldmark | Accepted | 2026-11-02 |
| [0026](0026-opentelemetry-prometheus-and-promtop.md) | Instrument with OpenTelemetry; serve metrics to Prometheus with the promtop dashboard, export traces over OTLP | Accepted | 2026-11-02 |
| [0027](0027-written-go-conventions-and-lint-guardrails.md) | Write the Go conventions down and enforce what a linter can | Accepted | 2026-11-02 |
| [0028](0028-samber-do-composition-root-in-config.md) | Assemble the node in a `config` package of `samber/do` providers | Accepted | 2026-11-02 |
| [0029](0029-urfave-cli-for-commands-and-bootstrap-flags.md) | Use urfave/cli v3 for commands and bootstrap flags, with `main.go` at the root and the command tree in `cmd` | Accepted | 2026-11-02 |
| [0030](0030-mustinvoke-inside-providers.md) | Take dependencies with do.MustInvoke inside providers (supersedes the `Must*` rule of ADR-0028) | Accepted | 2026-11-02 |
| [0031](0031-agent-security-baseline.md) | Adopt a security baseline for the agent: rules in code, session-wide taint, a clean-context check and grants for scheduled tasks | Accepted | 2026-11-02 |
| [0032](0032-buttons-and-resume.md) | Answer confirmations with one-time buttons and resume the conversation with a new event | Accepted | 2026-11-03 |
| [0033](0033-memory-in-the-workspace-git-with-the-git-binary.md) | Keep memory as Markdown files in the workspace's git repository, driven by the `git` binary (D-7) | Accepted; file layout superseded by 0071 | 2026-11-03 |
| [0034](0034-clean-context-guard.md) | Check every memory write with a cheap model in a clean context, failing closed | Accepted | 2026-11-03 |
| [0035](0035-web-reading.md) | Read pages through an html2md WASM module in Rust; search with Brave or DuckDuckGo | Accepted | 2026-11-03 |
| [0036](0036-mcp-bridge-over-a-per-run-unix-socket.md) | Give CLI providers the agent's tools over MCP on a per-run Unix socket with a token | Accepted | 2026-11-03 |
| [0037](0037-own-minimal-mcp-server.md) | Write a minimal MCP server of our own instead of using the official Go SDK | Accepted | 2026-11-03 |
| [0038](0038-host-commands-as-argv-with-classes-in-code.md) | Give the agent host commands as argv, classed by a table in code, in a milestone M3d | Accepted | 2026-11-03 |
| [0039](0039-command-sandbox.md) | Run host commands and skill scripts in a sandbox that fails closed | Accepted | 2026-11-03 |
| [0040](0040-plans-as-approvals.md) | Let one press on a plan approve exactly the commands it lists | Accepted | 2026-11-03 |
| [0041](0041-module-install-update-and-trust-boundary.md) | Install module updates from the project's GitHub Releases, checked, self-tested and switched without a restart | Accepted | 2026-11-03 |
| [0042](0042-skills-and-scripts-before-the-sandbox.md) | Run skill scripts with a declared effect class, an environment allowlist and a timeout until the sandbox lands | Accepted; two rules superseded by 0049 | 2026-11-03 |
| [0043](0043-logs-on-disk-with-an-own-rotator.md) | Write logs to disk through a small own rotator, synchronously | Accepted | 2026-11-03 |
| [0044](0044-ganglion-mcp-diagnostics-over-stdio.md) | Serve read-only diagnostics as `ganglion mcp` over stdio, with the own MCP server | Accepted; one line superseded by 0049 | 2026-11-03 |
| [0045](0045-metrics-in-the-chat-and-attachments-in-the-outbox.md) | Render the metrics picture in process with promtop, and keep attachments in the outbox row | Accepted | 2026-11-03 |
| [0046](0046-a-restart-is-not-a-failed-attempt.md) | Count a run a restart cut short apart from a failed attempt | Accepted | 2026-11-04 |
| [0047](0047-sonnet-answers-during-development.md) | During development, Sonnet answers and Haiku does compaction and checks (supersedes 0022) | Accepted | 2026-11-04 |
| [0048](0048-chatlab-over-the-fake-bot-api.md) | Test the agent by talking to it, through a separate program over the fake Bot API | Accepted | 2026-11-04 |
| [0049](0049-three-gaps-where-outside-text-and-private-data-cross.md) | Ask before a read script takes a tainted session's input, keep a local-only session's tools on the node, and mark `ganglion mcp`'s results as data | Accepted | 2026-11-05 |
| [0050](0050-additions-join-a-running-turn.md) | Let a message written during a turn join it at the next boundary, ending a CLI run to do so | Accepted | 2026-11-05 |
| [0051](0051-status-reactions-outside-the-outbox.md) | Show work that takes long by reactions set outside the outbox | Accepted | 2026-11-05 |
| [0052](0052-a-plan-is-delivered-during-its-turn.md) | Deliver a plan during its turn through the outbox, with its own table | Accepted | 2026-11-05 |
| [0053](0053-fixed-texts-in-the-persons-language.md) | Send fixed texts in the person's language, from a catalogue with a one-time machine translation | Accepted | 2026-11-06 |
| [0054](0054-structured-replies-as-rich-blocks-the-transport-builds.md) | Send structured replies as Telegram rich messages, from blocks the transport builds | Accepted | 2026-11-06 |
| [0055](0055-a-reply-carries-the-replied-messages-origin.md) | Let a reply carry the replied message's origin, from the node's own outbox | Accepted | 2026-11-07 |
| [0056](0056-small-hosts-as-a-soft-target.md) | Treat small hosts as a soft target held by GOMEMLIMIT, not as a 128 MB line | Superseded by ADR-0087 (small hosts as the aim) | 2026-11-08 |
| [0087](0087-distribute-to-any-machine-and-drop-small-hosts-as-a-target.md) | Aim the node at any machine it is distributed to, and drop small hosts as a target | Accepted | 2026-11-14 |
| [0057](0057-commands-may-list-split-folders.md) | Let commands list the folders the sandbox splits | Accepted | 2026-11-08 |
| [0058](0058-load-shedding-leaves-p1.md) | Take load shedding out of P1 and pass the gate without the two-week clock | Accepted | 2026-11-09 |
| [0059](0059-node-config-file.md) | Make `ganglion.yaml` the node's single source of configuration, with secrets as `env:NAME` (D-3) | Accepted | 2026-11-10 |
| [0060](0060-models-by-purpose.md) | Choose models by purpose, each with an explicit order and an optional effort | Accepted | 2026-11-10 |
| [0061](0061-local-modules-built-from-source.md) | Build `local/…` modules from source through a builder, install them on the owner's press, and journal every version (extends 0041) | Accepted | 2026-11-10 |
| [0062](0062-cells-owned-by-modules.md) | Let modules own the cells they declare, and prefix cell and tool names with the module's name (extends 0014) | Accepted | 2026-11-10 |
| [0063](0063-module-language.md) | Write modules in Rust by default, TinyGo when Go code already exists, standard Go only when neither fits | Accepted | 2026-11-10 |
| [0064](0064-builds-write-only-their-temp-folder.md) | Let a module's build write only its own temporary folder, and kill what it leaves running (refines 0039) | Accepted | 2026-11-09 |
| [0065](0065-cell-triggers-through-the-inbox.md) | Start a module when cells it listens to change, with the event carried by the node's inbox | Accepted; the inbox as the trigger's transport superseded by 0108 from M9 | 2026-11-10 |
| [0066](0066-notify-capability.md) | Let a module write to the owner's private chat through a `notify` capability with a daily limit | Accepted | 2026-11-10 |
| [0067](0067-agent-writes-module-source.md) | Let the agent write a module's source in the workspace without a press, from a clean session only (moves a part of 0061 from P3) | Accepted | 2026-11-10 |
| [0068](0068-vouch-for-module-source-per-file-and-vetted-crates.md) | Vouch for module source per file and only with the examples' crates (refines 0067) | Accepted | 2026-11-10 |
| [0069](0069-background-jobs-with-a-live-card.md) | Run long node work as background jobs with a live card the node edits | Accepted | 2026-11-10 |
| [0070](0070-builds-in-a-row-instead-of-depth.md) | Bound the agent's fix-and-rebuild loop by builds in a row, not by turn depth (refines 0069) | Accepted | 2026-11-10 |
| [0071](0071-memory-topics-whole-in-the-prompt.md) | Keep facts in topic files and show every topic whole in the prompt, within a budget (replaces the file layout of 0033) | Accepted | 2026-11-11 |
| [0072](0072-who-writes-memory.md) | The agent writes memory in the turn, and a review after quiet time catches what it missed | Accepted | 2026-11-11 |
| [0073](0073-a-modules-message-names-its-module.md) | A module's message records its module, and a reply to it names the module without its text | Accepted | 2026-11-11 |
| [0074](0074-a-modules-name-goes-under-its-message.md) | A module's name goes under its message as the node's signature, not before its text | Accepted | 2026-11-11 |
| [0075](0075-reading-a-modules-state-keeps-the-session-clean.md) | Reading a module's state keeps the session clean, and a write in a tainted session asks instead of refusing | Accepted; press superseded by 0111 | 2026-11-11 |
| [0076](0076-telegram-groups-and-the-addressing-rule.md) | Connect a Telegram group by the admin's press and answer in it only when addressed | Accepted | 2026-11-13 |
| [0077](0077-encrypted-secret-store-on-the-nodes-key.md) | Keep secrets in an encrypted store on the node's own key, scoped by module, with the environment kept as a source | Accepted | 2026-11-13 |
| [0078](0078-telegram-updates-by-polling-or-webhook.md) | Receive Telegram updates by polling or by a webhook, chosen in the node's file | Accepted | 2026-11-13 |
| [0079](0079-a-modules-messages-go-where-the-nodes-file-says.md) | Route a module's messages by the node's file, to named destinations | Accepted | 2026-11-13 |
| [0080](0080-keep-the-own-telegram-client-and-name-the-library-to-move-to.md) | Keep the own Telegram client, and name the library to move to | Accepted | 2026-11-13 |
| [0081](0081-update-the-node-from-signed-releases-at-the-owners-command.md) | Update the node from the project's signed releases, at the owner's command | Accepted; the rule that the launcher always runs the active release superseded by 0102 | 2026-11-13 |
| [0082](0082-open-the-repository-now-and-keep-the-announced-release-in-p3.md) | Open the repository now, and keep the announced release in P3 | Accepted | 2026-11-14 |
| [0083](0083-the-repository-holds-the-core-and-no-users-module.md) | The repository holds the core and no user's module | Accepted | 2026-11-14 |
| [0084](0084-publish-docs-with-zensical-on-github-pages.md) | Publish docs/ as a site with Zensical on GitHub Pages | Superseded by 0085 (scope) | 2026-11-14 |
| [0085](0085-the-site-publishes-the-user-guide-not-the-engineering-record.md) | The site publishes the user guide, not the engineering record | Accepted | 2026-11-14 |
| [0086](0086-publish-the-nodes-image-to-ghcr-on-a-core-tag.md) | Publish the node's image to GHCR on a core tag | Accepted | 2026-11-14 |
| [0088](0088-a-newer-question-replaces-an-older-one-of-the-same-tool.md) | Let a newer question replace a waiting one of the same tool from an earlier turn | Accepted | 2026-11-16 |
| [0089](0089-set-a-reminder-without-a-press-in-a-clean-session.md) | Set a reminder without a press in a clean session, with a Delete button | Accepted | 2026-11-16 |
| [0090](0090-a-module-is-tried-on-its-examples-before-install.md) | Try a module on its examples in a sandbox right after the build | Accepted | 2026-11-16 |
| [0091](0091-delete-the-owners-job-without-a-press-in-a-clean-session.md) | Delete the owner's reminder or task without a press in a clean session | Accepted | 2026-11-16 |
| [0092](0092-a-bounded-run-journal-per-module-with-the-chain-copied-in.md) | Keep a bounded journal of each module's runs, with the chain that started a run copied into it | Accepted | 2026-11-16 |
| [0093](0093-config-changes-through-the-agent-with-history-in-the-workspace.md) | Change the node's file through the agent on an allowlist, with its history in the workspace repository | Accepted | 2026-11-16 |
| [0094](0094-the-github-project-holds-the-plan-and-the-repository-holds-decisions.md) | Keep the plan and the status in the GitHub Project, and decisions in the repository | Accepted | 2026-11-17 |
| [0095](0095-daily-encrypted-backups-to-an-s3-compatible-bucket.md) | Back up the node daily, encrypted with a key from its master key, to an S3-compatible bucket | Accepted | 2026-11-17 |
| [0096](0096-host-commands-run-the-active-release.md) | Run a host command as the node's active release | Accepted; an image not older than the release runs the command, per 0102 | 2026-11-18 |
| [0097](0097-connect-remote-mcp-servers-from-the-chat-with-an-own-client.md) | Connect remote MCP servers from the chat with an own client and `x/oauth2`, in P1 | Accepted | 2026-11-18 |
| [0098](0098-hand-values-over-from-the-chat-through-a-pinned-static-page.md) | Hand a code or a secret over from the chat through a pinned static page and a pasted line | Accepted | 2026-11-18 |
| [0099](0099-the-cli-bridge-announces-tools-turned-on-during-a-run.md) | The CLI bridge announces tools turned on during a run | Accepted | 2026-11-19 |
| [0100](0100-on-demand-bookkeeping-spends-no-turn-budget-and-the-budget-is-20.md) | On-demand bookkeeping spends no turn budget, and the budget is 20 (amends the number in ADR-0036) | Accepted | 2026-11-19 |
| [0101](0101-a-servers-instructions-reach-the-model-as-data-and-its-notes-live-in-memory.md) | A server's instructions reach the model as data, and notes about it live in a memory topic | Accepted | 2026-11-19 |
| [0102](0102-the-images-binary-runs-when-it-is-not-older-than-the-active-release.md) | Run the image's binary when it is not older than the active release | Accepted | 2026-11-20 |
| [0103](0103-a-commands-answer-records-its-command-and-is-quoted-only-when-it-holds-the-nodes-words.md) | A command's answer records its command, and a reply brings its text only when it holds the node's words | Accepted | 2026-11-21 |
| [0104](0104-keep-ended-sessions-90-days-once-the-review-has-read-them.md) | Keep ended sessions 90 days once the memory review has read them, and leave the nightly model pass for later | Accepted | 2026-11-21 |
| [0105](0105-start-p2-with-the-bus-ports-edges-and-http-modules.md) | Start P2 with the bus, ports, edges and HTTP modules, and allow module pages on an edge's loopback in 0.x | Accepted | 2026-11-21 |
| [0106](0106-embed-nats-with-jetstream-as-the-bus-behind-narrow-interfaces.md) | Embed nats-server with JetStream as the bus, allow an external server or cluster, and reach it only through narrow interfaces | Accepted | 2026-11-21 |
| [0107](0107-keep-cells-in-a-jetstream-kv-bucket.md) | Keep cells in a JetStream KV bucket, the only store of their values | Accepted | 2026-11-21 |
| [0108](0108-ports-in-the-manifest-links-in-the-nodes-file.md) | Declare ports in the manifest, keep links in the node's file, and carry events on JetStream and calls on request/reply | Accepted | 2026-11-21 |
| [0109](0109-a-trust-chain-in-the-vault-and-edges-joined-by-a-one-time-code.md) | Keep the NATS trust chain in the secret store, give each edge its own permissions in one account, and join an edge by a one-time code with its key born on the edge | Accepted | 2026-11-21 |
| [0110](0110-modules-answer-http-on-an-edges-loopback.md) | Let a module answer HTTP on an edge's loopback listener, with the node owning the listener and the headers | Accepted | 2026-11-21 |
| [0111](0111-building-a-module-asks-once-at-the-install.md) | Build modules without a press on the way, and ask once, at the install, for every module and link of the request | Accepted | 2026-11-22 |
| [0112](0112-a-build-is-quiet-until-the-install-window.md) | Keep a module build quiet until the install window: no text while a build is awaited, one card per module | Accepted | 2026-11-22 |

Open questions that will become ADRs are listed in
[spec section 11](../spec.md#11-open-decisions).

## Writing a new ADR

1. Copy [template.md](template.md) to `NNNN-short-title.md` with the next
   number.
2. Fill in the context and options first; compare before deciding.
3. Add a row to the table above.
4. Accepted ADRs are not rewritten. To change a decision, write a new ADR
   and mark the old one "Superseded by ADR-NNNN".
