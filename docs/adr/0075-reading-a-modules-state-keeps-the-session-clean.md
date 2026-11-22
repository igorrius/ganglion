# ADR-0075: Reading a module's state keeps the session clean, and a write in a tainted session asks instead of refusing

- **Status:** Accepted; the press of `module.write` in a tainted session is superseded by [ADR-0111](0111-building-a-module-asks-once-at-the-install.md)
- **Date:** 2026-11-11
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-13, R-RUN-14, R-RUN-17), [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-6); changes the refusal decided in [ADR-0067](0067-agent-writes-module-source.md); board card G-147

## Context

On 2026-11-11 the owner replied to a module's message and asked to translate
its texts. The agent found the module (ADR-0073), read its source, prepared
the whole file, and `module.write` refused: the session held outside content.
The node's journal shows where the taint came from. Three hours earlier, in
the same session, the agent had called `module.status` once, and its answer
was always outside content ("the settings' values come from a file the agent
did not write"). That answer held no value at all: two settings, neither set.
The session lives until six quiet hours pass, so one look at a module's state
closed `module.write` for the day, and the agent learned it only after doing
the work.

Rewriting a module on the owner's request is the project's main loop. The
rule that stood in its way has a sound core (outside text must not dictate
code whose install window shows only its rights, ADR-0067) and two edges that
are too wide:

- what counts as outside content: an answer made of the node's own words,
  counts and checked names cannot carry an instruction;
- what a tainted session may do: a refusal with no way forward but `/new`,
  where every other write in a tainted session asks the owner (memory,
  R-POL-6).

Cells already follow the first idea: a cell's value taints only when it holds
a string (R-RUN-13).

## Options

### A. Leave both; teach the agent to ask for `/new` first

A directive. The agent still cannot know the session is tainted by something
as harmless as `module.status`, and the owner pays a `/new` for every look.

### B. `module.status` is outside content only when it shows a setting as text

A value that is hidden (`<set>`), or a number or a boolean of its declared
type, is not text. A string value, or a "number" that does not parse as one,
still makes the answer outside content. Everything else in the answer is
already the node's words, a closed set of kinds, counts, and names checked
against the cell and module name patterns.

### C. `module.write` in a tainted session asks the owner

The policy turns a tainted `module.write` into a question: the file, its
size, how many lines are new or changed, and that the session holds outside
content. On the press the file is written, and taken out of the node's record
of its own files, so a read or a build of the folder answers as outside
content until a clean session writes the file again. A tainted call that
reaches `Run` without a press (a scheduled task's under its grant) is refused
as before.

### D. Lift the rule: write in any session

No question. An instruction in a web page could then rewrite a module that
the owner installs on the rights window alone.

## Comparison

| Criterion | A | B | C | D |
|---|---|---|---|---|
| A look at a module's state leaves it writable | no | yes | with a press | yes |
| Outside text cannot dictate code unseen | yes | yes | the owner is asked | no |
| A tainted session has a way forward without `/new` | no | only when the taint was a false one | yes | yes |
| Stopped by code, not by a prompt | no | yes | yes | — |
| Effort | a directive | one predicate | a policy rule, a question, the record | none |

## Decision

**B and C together.** B removes the false taint that caused the incident. C
gives a truly tainted session a way forward that is the owner's choice, as
for memory. The owner asked for both (2026-11-11).

The question does not show the code: it may be 256 KB, and on a phone a
count of changed lines tells a one-line fix from a rewrite better than a
diff nobody reads. The install window, with its rights, still follows.

## Security

- **Trust boundaries:** the boundary of ADR-0067 moves from "never" to "on
  the owner's press"; the boundary of R-RUN-13 (what is outside content) is
  narrowed for `module.status` by value type.
- **What an attacker controls:** a web page, a forwarded message, a module's
  text, the manifest's key names and defaults, a settings value someone put
  in `ganglion.yaml`.
- **Abuse cases:**
  - *A settings value carries an instruction.* A string value is shown and
    makes the answer outside content; a value under a "number" key that is
    not a finite number, or under a "boolean" key that is not `true` or
    `false`, does too. Test:
    `TestModuleStatusIsOutsideOnlyWhenItShowsASettingAsText`.
  - *A manifest's key name or default carries an instruction.* Unchanged: a
    key that is not plain is not shown, a default never is
    (`TestModuleStatusGolden`).
  - *A page tells the agent to rewrite a module.* The session is tainted, so
    the write asks; the owner sees the module, the file and the size of the
    change. Test: the policy's "module write, tainted, confirms".
  - *A tainted write is laundered as the node's own code.* The file leaves
    the record, so `module.read` and the build's answer of that folder are
    outside content, and stay so until a clean session writes the file
    again. Test: `TestAPressedWriteInATaintedSessionIsNotTheNodesOwn`.
  - *A scheduled task writes a module from a tainted run.* A task's granted
    call is not asked (R-POL-8), so `Run` refuses a tainted call without a
    press. Test: `TestModuleWriteRefusesATaintedCallWithoutAPress`.
- The install window and the rights it shows are unchanged: a module still
  gets only what the owner presses for.

## Consequences

- The agent can look at a module and rewrite it in one session.
- In a tainted session each file costs the owner a press; the skill tells the
  agent to say so and suggest `/new` for more than one small file.
- A module with a file written on a press builds with its whole answer as
  outside content, so the model sees less of the build's diagnostics as its
  own until a clean rewrite.
- `module.status` on a module with a string setting (an inverter's serial
  number) still taints. Typed settings do not.

## Revisit when

A string setting keeps tainting sessions in practice (then show such values
only to the owner, as a cell's `about` is), or a press per file proves too
many for a real rewrite (then one press for a set of files, as a plan).
