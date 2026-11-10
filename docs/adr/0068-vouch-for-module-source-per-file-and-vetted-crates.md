# ADR-0068: Vouch for module source per file and only with the examples' crates

- **Status:** Accepted
- **Date:** 2026-11-10
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-13, R-RUN-17); refines [ADR-0067](0067-agent-writes-module-source.md); [M4c design](../plans/p1-m4c-reflexes-design.md) section 6; [ADR-0049](0049-three-gaps-where-outside-text-and-private-data-cross.md)

## Context

ADR-0067 lets a build answer reach the model as clean text when the module
folder matches a record of what the node's own tools left, and says the
record is one digest of the folder. Building it showed two gaps:

- A folder digest blesses whatever else is in the folder at the time of
  the next write: a file changed by hand stays unnoticed once the agent
  writes any other file, because the write refreshes the one digest.
- The build's output is not only rustc's diagnostics about the module's
  files. A dependency's build script or procedural macro runs during the
  build and can write any text into that output, so a clean answer is only
  as trustworthy as the dependencies are.

## Options

### A. Text filter only

Keep rustc's lines about the module's own files and drop the rest, with
the folder digest of ADR-0067. Dependencies are whatever the lock says.

### B. JSON diagnostics from cargo

Run cargo with `--message-format=json` and read only the compiler messages
whose primary span lies in the module's files. Dependencies are as in A.

### C. The examples' crates only

As A or B, and in addition the answer is clean only while the module's
`Cargo.lock` lists, besides its own package, only crates.io crates (name,
version, checksum) that the `Cargo.lock` of an example the node ships
lists too. Anything else keeps the answer outside content.

### D. Provenance per file or per folder

The record holds one digest for the folder, or one digest for each file
the node's tools wrote, with where the last write came from and when.

## Comparison

| Criterion | A | B | C | D per folder | D per file |
|---|---|---|---|---|---|
| Text a dependency's build script can write into the answer | Yes | Yes (cargo's JSON stream carries build-script output as messages too) | No: only vetted crates run | n/a | n/a |
| Changes to the build's output format break it | Rarely | Yes, the JSON schema | Rarely | n/a | n/a |
| A hand edit stays noticed after a later `module.write` | n/a | n/a | n/a | No | Yes |
| Effort | Low | Medium | Low | Low | Low |
| Modules that can use a new crate cleanly | Any | Any | Only the vetted set | n/a | n/a |

## Decision

- **Filter:** the text filter of A (rustc's diagnostics about the module's
  own files).
- **Crates:** C, chosen by the owner (2026-11-10): the answer is clean only
  while the module's `Cargo.lock` lists, besides its own package, crates
  that one of the examples' locks lists with the same version and
  checksum. A missing or unreadable lock, or any other dependency, keeps
  the answer outside content. B was not chosen: it does not remove the
  dependency risk and ties the filter to cargo's JSON schema.
- **Record:** D per file. The record in
  `<data>/modules/provenance/<name>.json` holds the digest of each file the
  node's tools wrote, the origin of the last write (`agent` or
  `example:<name>`) and its time. A write of one file does not bless
  another file's change. It is read with a size cap.

ADR-0067 stays as it is; this ADR replaces its "digest of the folder" with
the per-file record and adds the crate rule.

## Security

- **Boundary:** a build's output, text produced by code the node did not
  write, reaches the model as clean text.
- **What an attacker controls:** the module folder (by other means than
  the node's tools), the text a dependency's build script or macro writes
  into the output, a doctored `Cargo.lock`.
  - A file changed by hand next to a later `module.write` → its digest no
    longer matches its record, so the answer is outside content.
  - A dependency writing text into the build's output → only crates the
    examples already lock run, and the lock must match name, version and
    checksum; a doctored or unreadable lock keeps the answer outside
    content.
  - A huge record or lock used to exhaust memory → both are read with a
    size cap.

## Consequences

- A module that needs a crate no example locks still builds, but its build
  answers are outside content: the first one taints the session. Adding a
  crate means adding it to an example's lock.
- The record is larger (a digest per file, up to 200 files), hence its cap
  of 256 KiB.
- A hand edit of one file costs clean answers until that file is written
  by the agent again, not until any file is.

## Revisit when

Modules are signed (P3), a builder worker with its own cache appears (P2),
or the set of vetted crates becomes a burden for real modules.
