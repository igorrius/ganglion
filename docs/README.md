# ganglion docs

Start here when coming back to the project.

## Where we are

- **Phase:** P0 · Recon ([plan](spec.md#10-plan-phases-and-gates))
- **Done:** 0.1 repository skeleton — `cmd/ganglion`, interface packages, CI;
  0.2 Deye reader in WASM on wazero — on 2026-11-01 the module (Go and TinyGo
  builds) returned the same sample as `deye.py`, 12 of 12 fields, same
  collection time
  0.3 module ABI and toolchains — own ABI chosen
  ([ADR-0011](adr/0011-module-abi.md)); Rust and TinyGo modules are 5–20×
  smaller than standard Go; 0.4 MCP and A2A coverage of the protocol —
  [research note](research/2026-11-01-mcp-a2a-coverage.md), direction decided in
  [ADR-0013](adr/0013-protocol-envelope-around-stateless-mcp.md)
- **Next:** 0.5 runner RAM on a Pi; 0.6–0.7 Tuya access and home internet
  during outages
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
