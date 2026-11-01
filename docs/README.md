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
- **P1 progress:** M1 state cells done — `cells`, `cells/sqlstore`,
  `cells/virtual`, `sqlitedb` ([plan](plans/p1-m1-cells.md)).
- **Next:** M2 delivery and Telegram, then the agent (M3) — agent first
  after its foundations ([ADR-0015](adr/0015-agent-first-p1.md)).
- **Open decisions:** [spec section 11](spec.md#11-open-decisions)

Update this block when a task or phase closes.

## Map

| Question | Read |
|---|---|
| What are we building, what is in scope now, what are the requirements? | [spec.md](spec.md) |
| What does the system look like, from the big picture down to packages? | [architecture/](architecture/README.md) |
| Why was it decided this way, and what else was considered? | [adr/](adr/README.md) |
| Where is X in the code, and does it exist yet? | [architecture/05-code-map.md](architecture/05-code-map.md) |
| What did we find out before deciding? | [research/](research/) |
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
