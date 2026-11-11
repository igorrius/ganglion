# ADR-0074: A module's name goes under its message as the node's signature, not before its text

- **Status:** Accepted
- **Date:** 2026-11-11
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-16), [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-11); changes the form of the message decided in [ADR-0066](0066-notify-capability.md); uses the blocks of [ADR-0054](0054-structured-replies-as-rich-blocks-the-transport-builds.md)

## Context

A module's message went out as `<name>: <text>` in one line (ADR-0066). The
owner's first messages from a module he had asked for read "battery-alerts:
🚫 The grid is gone", and he did not want the name in front of the text
(2026-11-11): it is service information, it gets in the way, and it will not
fit a module that formats its messages.

The name has a job: the text is the module's, not the node's, and the owner
must always see who wrote it. A module must not be able to remove, change or
imitate it.

## Options

### A. Keep the prefix

`<name>: <text>`. Nothing to build. The owner reads the name first, every
time.

### B. The name as the last line, in italics

An ordinary message: the text, an empty line, the name in italics. Works in
every app. It is still part of the same text, in the same size.

### C. The name in the footer block of a rich message

The message goes as a rich message built from blocks (ADR-0054): the text's
blocks, a divider, then a `footer` block with the name. Telegram shows a footer apart
from the text. When rich messages are off or Telegram refuses one, the
message goes as B.

## Comparison

| Criterion | A. prefix | B. italic line | C. footer, else B |
|---|---|---|---|
| The text starts with the module's words (owner) | no | yes | yes |
| The name reads as service information (owner) | no | partly | yes |
| The node writes the name; the module cannot | yes | yes | yes |
| Works when the app or the server has no rich messages | yes | yes | yes, as B |
| Room for modules that format their text later | poor | fair | good |
| Effort | none | small | small: one block kind, one field |

## Decision

**C.** It is what the owner asked for, and it keeps the name the node's:
`event.Outgoing.Signature` is set by the agent from the module's name, the
transport builds the footer from that field alone, and `renderRich` cannot
make a footer from any text. Trial messages went to the owner's chat
through the test bot on 2026-11-11 and Telegram accepted them; of a bare
footer, an italic last line and a footer under a divider, the owner chose
the divider with the name in italics in the footer, and asked for the
running version beside it: `battery-alerts v2`. The version is the number
the module host loaded, carried in the `module.notify` event; one that is not
a number is left out.

A module's text stays plain: escaped, one line, no preview (R-RUN-16).
Formatting in a module's text is a separate decision.

## Security

- **Trust boundaries:** none new. The signature crosses from the agent to
  the transport as a field of the outgoing message, never as text.
- **What an attacker controls:** a module's text.
- **Abuse cases:**
  - *A module writes another module's name where the signature would be.*
    Its text is one line (line breaks folded) and escaped, so in the fallback
    form it cannot make the italic last line; in the rich form its text is in
    paragraph blocks and cannot be a footer. Tests:
    `TestModuleNotifyFoldsLineBreaks`, `TestOnlyASignatureMakesAFooter`.
  - *A model's answer makes a footer.* The allowlist of blocks a text can
    turn into has no footer (ADR-0054); only `signed` adds one.
  - *A rich message brings link previews back.* A signed message's text is
    escaped, so it holds no link; the fallback keeps previews off.
- The name is checked when the module is installed
  (`modstore.CheckShortName`).

## Consequences

- A module's message is a rich message where the setting is on: it depends on
  the owner's app showing rich messages, with the ordinary form as the way
  out.
- The copy the outbox keeps for replies holds the text without the name; the
  name is in the origin (ADR-0073).
- The fake Bot API accepts a footer block and records it.
- A signed message longer than one message would carry the signature on each
  part; a module's text is cut at 1000 characters, so there is one part.

## Revisit when

Modules get formatted text, the owner's apps show the footer badly, or a
transport other than Telegram carries module messages.
