# ganglion

Distributed agent network in Go. Start from [docs/README.md](docs/README.md):
it says where the project is and links the spec, the architecture views and
the ADRs.

- [docs/spec.md](docs/spec.md) — what we build, requirements (`R-<area>-<n>`),
  phases with gates. When a decision changes, update the spec first, then the
  code.
- [docs/architecture/](docs/architecture/README.md) — C4 views from context
  down to the code map.
- [docs/adr/](docs/adr/README.md) — why things are the way they are.
- [docs/research/](docs/research/) — dated research notes that ADRs cite.
- [docs/conventions.md](docs/conventions.md) — how Go code is written here:
  read it before writing or changing code.
- [docs/plans/](docs/plans/) — the phase plan and per-milestone
  implementation plans; tick tasks off as they are done.

## Rules

- **All documentation is written in English**: README, everything under
  `docs/`, code comments, doc comments, and Markdown files in the repository.
  This holds even when the conversation is in another language.
- Stay inside the current phase of the spec. An idea outside it goes to the
  spec's "Not now" list instead of into the code.
- **Significant decisions need an ADR** before or together with the code: a
  new dependency, a protocol or storage format, a runtime, a security
  boundary, a change to the phase plan. Compare at least two options. Copy
  `docs/adr/template.md`, add the row to `docs/adr/README.md`. Accepted ADRs
  are not rewritten; a changed decision is a new ADR that supersedes the old
  one.
- **Keep the architecture docs in sync with the code**, in the same commit:
  adding, moving or implementing a package updates
  `docs/architecture/05-code-map.md`; changing how components connect updates
  the diagram on the matching level page.
- When a spec task or phase closes, update "Where we are" in
  `docs/README.md`.
- Docs format: standard Markdown links with relative paths (no
  `[[wikilinks]]`), diagrams as Mermaid `flowchart`, `sequenceDiagram` or
  `stateDiagram-v2`, planned parts drawn dashed.
