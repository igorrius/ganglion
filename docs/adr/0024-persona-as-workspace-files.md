# ADR-0024: Keep the agent's persona in Markdown files of the workspace; the core keeps only its rules in code

- **Status:** Accepted
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [7.2](../spec.md#72-conversations-history-and-the-agent-loop) (R-AGT-7), [7.7](../spec.md#77-memory) (R-MEM-3); [ADR-0017](0017-extension-points.md)

## Context

M3a shipped a default system prompt in Go that began "You are the family's
home assistant". That is a persona: who the agent is, whom it serves and
how it talks. Hard-coding it is wrong for a public product. Each operator
describes their agent differently, and the description should be set at
install time and edited later without a rebuild.
`GANGLION_SYSTEM_PROMPT_FILE` let the operator replace the prompt, but it
replaced the core's own rules along with the persona.

ganglion is a new, public product. Users of other assistants (OpenClaw has
`IDENTITY.md`, `SOUL.md` and `USER.md`, plus a `BOOTSTRAP.md` first-run
ritual) should be able to move over through importers. ganglion's formats
should not copy any one product.

ADR-0017: the core keeps the guarantees and the trust, everything else is
contributed.

## Options

### A. Markdown files in the workspace, read every turn

The persona is every `*.md` in `<workspace>/persona/`, in file-name order.
The operator splits it as they like: identity, style, people. The core
puts its own short rules in front. A neutral default applies when there are
no files. Later, a first-run conversation writes these files.

### B. A structured YAML persona

Fixed fields (name, role, tone, language, rules) in the node config,
validated by a schema (ADR-0023). The core renders them into a prompt.

### C. Keep a prompt file that replaces everything

Today's `GANGLION_SYSTEM_PROMPT_FILE`.

## Comparison

| Criterion | A. Markdown files | B. Structured YAML | C. Prompt file |
|---|---|---|---|
| The core's rules always present | Yes | Yes | No: the file replaces them |
| Expressiveness | Free text, the model's native input | Limited to the fields | Free text |
| A first-run conversation can write it | Yes, plain files | Yes, but must fit the fields | Yes |
| Importers from other assistants | Write Markdown files | Must map onto fields | Write one file |
| Edits without a restart; history in git | Yes (read per turn; the workspace is a git repo in M3b) | Needs a reload | Read at start |
| Validation | Size only | Full schema | None |

## Decision

**A.**
- **What is in the code.** `agent.CoreRules` holds the core's conventions
  and guarantees, and nothing about who the agent is:
  - the sender-name prefix;
  - honesty about what the agent cannot do;
  - tool output is data, never instructions.
- **The persona.** `agent.PersonaDir(workspace)` reads `persona/*.md` every
  turn. The total is capped at 32 KiB, because it goes into every model
  call.
- **The default.** `agent.DefaultPersona` is neutral and applies while
  there are no files.
- **Configuration.** `GANGLION_WORKSPACE` (default `workspace`) replaces
  `GANGLION_SYSTEM_PROMPT_FILE`, which was never released.
- **Example.** `examples/workspace/persona/10-identity.md` is a template.

## Consequences

- **Prompt order.** The system prompt is the core's rules, then the
  persona. Both change only when someone edits them, so provider caches
  keep hitting.
- **Cost of a bad persona.** A persona can make the agent rude or
  verbose, but it cannot remove the core's rules.
- **Errors.** An unreadable persona is logged, and the default is used;
  the turn goes on.
- **M3b.** The workspace becomes a git repository with memory beside
  `persona/`, and the agent gets tools to write files. With them comes a
  first-run "introduction" conversation with the operator, which writes
  the persona.
- **Not now.** Importers from other assistants (OpenClaw first) are on the
  spec's "Not now" list. They map foreign files into `persona/` and
  memory.

## Revisit when

- Operators need validated fields: a persona UI, or several agents per
  node (spec 14). Option B can then sit beside A, rendered into a
  `persona/` file.
