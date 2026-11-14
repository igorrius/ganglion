# ADR-0083: The repository holds the core and no user's module

- **Status:** Accepted
- **Date:** 2026-11-14
- **Deciders:** Igor Artasevych
- **Spec:** [2. Principles](../spec.md#2-principles),
  [8. First reflex](../spec.md#8-first-reflex-deye-p1)

## Context

ganglion is a core that builds what its user asks for: the agent writes a
module, the node compiles it and runs it with no model
([ADR-0063](0063-module-language.md), R-RUN-9). The project grew up on one
such module, the reader of the owner's Deye inverter, and it left three
traces in the repository:

- `internal/deyeref`, a Go reader ported from the owner's old script in P0
  to choose the module ABI and to compare toolchains (ADR-0011). It was
  never shipped; since M4b it only served as the answer the Rust example was
  compared with.
- `examples/modules/deye-reader`, the Rust module the agent copies, builds
  and installs (R-DEYE-1). It is the only example of a module that polls a
  service, keeps secrets, writes cells and answers a tool.
- Deye as the sample in tests, lab scenarios, CI, benchmarks, comments and
  section 8 of the spec.

On the day the repository opened ([ADR-0082](0082-open-the-repository-now-and-keep-the-announced-release-in-p3.md))
a code scanner flagged the two readers, and the owner asked why a reader of
his own device is part of the project at all. It is his personal module. A
reader of the repository now sees an inverter where the product is the core
that would build such a module for anyone.

What the example does carry, and must not be lost, is knowledge: how a
module that reads an outside service is written (a schedule, a rate limit
honoured, a refused login never retried, cells with their age, a tool that
answers within its contract), and how modules are linked through cells. The
agent has that knowledge today only by copying the example.

## Options

### A. Keep Deye as the project's worked example

Nothing changes. The Go reader stays as the reference, the Rust module as
the example.

### B. Remove the Go reader, keep the Rust module as the example

The repository keeps one device-specific module, named as an example of a
module built at a user's request.

### C. Nothing of one user's in the repository; templates and skills carry the knowledge

The repository holds the core, neutral templates the agent starts a module
from, and built-in skills that say which template to take, when, and how to
write, check and link modules. Built-in skills are embedded in the binary,
so they are updated with the node. Deye leaves in two steps: the Go reader
now, the Rust example when a neutral template and the skills have taken its
place.

## Comparison

| Criterion | A. Keep | B. Go reader out | C. Templates and skills |
|---|---|---|---|
| What a reader takes the product for | An inverter monitor with an agent | The same, less so | A core that builds modules |
| Where the knowledge of writing a reader module lives | In one device's code, to be copied and gutted | The same | In a template that names no device and a skill that explains the choices |
| How that knowledge reaches a running node | A new image with the example folder | The same | With the binary, as every built-in skill does |
| What tests, CI and the lab build | The owner's module | The owner's module | A module that belongs to the project |
| Effort | None | Small: one package, two tests, the Makefile | B, then a template, the skill, and moving the tests, CI job, lab scenarios and spec section 8 off Deye |
| Risk | None | The comparison with a second implementation is lost; a recorded sample replaces it | The same, and the lab scenarios that pass today have to pass on another module |

## Decision

**C**, in two steps.

1. **Now:** `internal/deyeref`, its Go and TinyGo builds and the comparison
   with it are removed. The Rust example is checked against the sample
   recorded from the same answers.
2. **Next (P1, task 6.8):** a neutral reader template beside
   `reflex-template`, and the built-in skill `module-writing` grown to say
   which template fits which request and how modules are linked. Then
   `examples/modules/deye-reader` leaves, and the tests, the CI job, the lab
   scenarios and section 8 of the spec move to the template. The owner's
   module stays where it runs: in his node's workspace.

The rule from now on (spec principle 10): a module, a reader or a fixture
for one person's device or account is not added to the repository. What a
feature needs in order to be shown is a neutral example.

## Security

No new boundary. The module sandbox, the builder and the approval before an
install (R-RUN-9) are what they were; a template is built and installed the
same way an example is. One thing gets better: a built-in skill is part of
the signed binary, while an example folder is a set of files beside it.

## Consequences

- The repository no longer has a second implementation to compare a module
  with. The recorded sample pins the same values.
- `make modules` builds `html2md` only; `make modules-tinygo` is gone. The
  toolchain comparison of P0 stays in ADR-0011 and its research note.
- Until step 2 the Rust example and its names in tests and documents
  remain.
- Step 2 has to be built: the template, the skill text, and the move of
  about a dozen lab scenarios.
- Accepted ADRs and closed plans that describe Deye are history and are not
  rewritten.

## Revisit when

A feature cannot be shown or tested without a real device's module, or the
neutral template turns out to teach the agent worse than the example did
(measured in the lab: the scenarios that build and install a module).
