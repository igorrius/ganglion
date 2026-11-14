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
- **Nothing real about a running node goes into the repository** (owner,
  2026-11-14): not in code, tests, docs, plans, ADRs, commit messages or
  pull requests. That covers the address or domain of a live node, a bot's
  name, Telegram ids of people, groups and topics, the names of a
  platform's project and service, serial numbers and account names of
  devices, and any secret. Real values are used while working (a deploy, a
  live check) and stay in the environment, the node's own files and the
  session's notes. A text that needs one takes a placeholder:
  `example.com`, `telegram:user:123456`, `telegram:-1001234567890`,
  `@test_bot`, `<service>`. A live check is recorded by what was done and
  what was seen, without the address it was done on.
- **Work is tracked in GitHub Issues** and on the
  [ganglion project](https://github.com/users/igorrius/projects/13). A bug
  or open item found in a review, a lab run or a live check becomes an
  issue (English; labels: type `bug`/`enhancement`/`chore`/`decision`,
  priority `P0`–`P3`, `area/<package>`; the milestone of the phase). A PR
  that closes one says `Fixes #N`. Decisions still go to the spec and ADRs;
  `docs/plans/` keeps the step-by-step plans. Issues #38–#174 came from
  the old backlog board (G-ids); that board is read-only now.
- **Pick the cheapest model that does the job.** At the start of a task,
  say in one line which model it needs; if the session runs a heavier one
  than that, say so, so the owner can switch. Pass `model` explicitly to
  every subagent:
  - **Opus** (or the top model when Opus is stuck): design, ADRs, spec
    changes, concurrency and security reasoning, debugging a failure whose
    cause is unclear, reviewing a risky change.
  - **Sonnet**: implementing a written plan, tests, keeping docs in sync,
    translation, migrations, routine reviews and live checks.
  - **Haiku**: searches and lookups, inventories, formatting, and
    mechanical bulk steps whose input is already prepared.
  - No model at all when a script does it: bulk `gh` calls, renames,
    format fixes.
- Docs format: standard Markdown links with relative paths (no
  `[[wikilinks]]`), diagrams as Mermaid `flowchart`, `sequenceDiagram` or
  `stateDiagram-v2`, planned parts drawn dashed.
