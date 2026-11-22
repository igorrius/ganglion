# ADR-0112: Keep a module build quiet until the install window: no text while a build is awaited, one card per module

- **Status:** Accepted
- **Date:** 2026-11-22
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-9, R-RUN-17), [R-RDR-5](../spec.md#8-first-reflex-a-reader-p1); follows [ADR-0111](0111-building-a-module-asks-once-at-the-install.md) and [ADR-0069](0069-background-jobs-with-a-live-card.md)

## Context

ADR-0111 made a build ask the owner once, at the install. The live check of #317 on v0.9.2 showed
that it did not make the build quiet. One plain request built two modules with six builds in about
four minutes, and the chat got:

- a card per build (six), each a message of its own;
- a text from the agent at the end of every turn: the first turn, and one turn per
  `[build finished]` note (nine), each resumed by `module.built` (ADR-0069) and ending, like every
  turn, in a reply.

The owner: still far too much talk for one request (#352). People ask for an outcome (R-RDR-5); how
many builds it took is not theirs to follow.

## Options

### A. The node keeps quiet

A turn that ends while its session still waits for a build of its own sends no text: the build's
note will resume the conversation, and the text stays in the history. A rebuild of a module edits
that module's earlier card in the conversation instead of sending a new one.

### B. The skill tells the agent to answer with nothing

The skill asks for an empty answer while a build runs; the node drops an empty reply.

### C. One card per request

One card lists every module and build of the request, edited in place.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Holds whatever the model writes | yes | no: a model often writes anyway | yes |
| The node knows what it needs | the session's awaited builds, the module's last card | nothing | which builds form a request, which it learns only at the install |
| Messages for two modules, six builds | 2 cards, the window, 1 line | depends on the model | 1 card, the window, 1 line |
| Effort | small | smallest | a request id through import, write and build |

## Decision

A (owner, 2026-11-22: "build it and install it, and that is all"). It holds by code, not by a
prompt, and it uses what the node already keeps. C may follow when the agent names a request before
it builds.

- **Quiet turns:** at the end of a turn, when the session's list of awaited jobs holds a build that is
  queued or running, the turn's text joins the history and is not sent. A question the turn asked (a
  confirmation card) is sent as before; so is the reply of a turn that ends with no build awaited:
  the install window, the line after the install, or a stop that needs the owner (a sixth build in a
  row, a refusal).
- **One card per module:** a build of a module in a conversation whose earlier build there already has
  a card edits that card (back to running, then to the new end) instead of sending a new one. The
  card names the module and how many builds it took.

## Security

No new boundary. The text that is not sent stays in the history the model reads; nothing the owner
must decide is held back, because a question goes out as a card, not as the turn's text, and a turn
with no awaited build replies as before. A module cannot make a turn quiet: only the node's own build
queue puts a build on the awaited list.

## Consequences

- The owner sees, for a request, one card per module, the install window and one closing line.
- A turn's text written while a build runs is visible only in the history (`/why`, the journal).
- The agent's progress words while it builds are lost to the owner on purpose; the card is the sign
  of life.

## Revisit when

A request needs a word to the owner while a build still runs, or the agent learns to name a request
before it builds (option C).
