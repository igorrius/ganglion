# ADR-0067: Let the agent write a module's source in the workspace without a press, from a clean session only

- **Status:** Accepted
- **Date:** 2026-11-10
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-9, R-RUN-13, R-RUN-17), [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-6), [9.4](../spec.md#94-skill-compilation-p3); [M4c design](../design/p1-m4c-reflexes-design.md) section 6; moves a part of [ADR-0061](0061-local-modules-built-from-source.md) from P3 to M4c; [ADR-0049](0049-three-gaps-where-outside-text-and-private-data-cross.md), [ADR-0064](0064-builds-write-only-their-temp-folder.md)

## Context

ADR-0061 built the path source → build → window → press in M4 and left
"the model writes the source" to P3. In M4b the agent could only copy an
example from the repository (`module.import`). M4c needs the agent to
write `deye-alerts` itself from the owner's words: that is the point of
reflex modules (M4c design 1).

Facts:

- A build runs in the confined sandbox and writes only its temporary
  folder (ADR-0064); a built module runs only after the owner's press on
  the window, which shows its rights but not its code.
- The build's answer to the model (the compiler's log, or the plan with
  the manifest's rights) is marked outside content, so the first failed
  build taints the session.
- In a tainted session side effects need confirmation (R-POL-6).

## Options

### A. A write tool without a press

`module.write` writes one file inside `workspace/modules/<name>/`, with
path and size checks, and commits each write.

### B. A press for every write

### C. One press to start a module, writes inside it free afterwards

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Presses in a write → build → fix loop | None | One per file per round | One |
| What the press shows that the window does not | — | The code, read by a person | A name |
| Runs code before the owner's install press | No | No | No |
| Fits the "the model builds chains" goal | Yes | No | Yes |

## Decision

**A** (owner, 2026-11-10). Source does nothing by itself: it is built in
the sandbox and runs only after the install press. A press per write
turns a fix loop into a dozen confirmations without telling the owner
more than the window does.

Two rules close the paths A would open:

1. **Clean session only.** `module.write` is refused, not confirmed, in a
   tainted session: outside text must not dictate code whose window shows
   only rights. The refusal is in the node's words.
2. **Provenance.** The node records each module folder's digest as its
   own tools left it (`module.import` from the repository's examples,
   `module.write` from a clean session). A build of a folder that matches
   its record answers without tainting, keeping only rustc's diagnostics
   about the module's own files; any other change to the folder makes the
   answer outside content again. `module.read` follows the same rule.

ADR-0061 stays: it is Accepted and its path is unchanged. This ADR moves
only "the model writes the source" from P3 into M4c; signatures and the
builder worker stay in P3.

## Security

- **Boundary:** model output → code the node builds and, after a press,
  runs.
- **What an attacker controls:** outside text in the session; files
  changed by other means; dependencies' build output.
  - Injected text dictating code → refused in a tainted session.
  - Compiler output used to launder outside text into a clean session →
    only folders the node's tools wrote from clean sessions count as the
    agent's own, and only rustc's diagnostics about their files reach
    the model clean.
  - Writing outside the folder → relative paths only, no `..`, hidden
    segments, `target/` or symlinks; size and file-count limits.
  - A module doing more than the owner meant → the window's rights, the
    press, and least-rights rules in the skill; code cannot reach beyond
    its manifest (R-RUN-1).

## Consequences

- The agent can turn a request into a module in one turn, ending at the
  window.
- The workspace history shows every write as its own commit.
- A module folder edited by hand loses its clean provenance until the
  agent writes it again; that is deliberate.

## Revisit when

Modules are signed (P3), a builder worker appears (P2), or a module needs
dependencies beyond the template's lock.
