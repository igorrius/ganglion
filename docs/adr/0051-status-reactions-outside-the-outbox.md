# ADR-0051: Show work that takes long by reactions set outside the outbox

- **Status:** Accepted
- **Date:** 2026-11-05
- **Deciders:** Igor Artasevych
- **Spec:** [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-4, R-DLV-5), [7.2](../spec.md#72-conversations-history-and-the-agent-loop) (R-LIVE-7); [live turns design](../plans/p1-live-turns-design.md), section 4

## Context

The owner wants to see that work goes on, and that an addition was noticed, without being
pestered. "typing…" already says "I am working", always. A reaction should say what it does
not: **this is taking long**. The owner's rule: only when the answer is not quick, and no
status said twice.

Telegram gives a bot one reaction per message, from a fixed list, and a new one replaces the
old. A throwaway build on the test bot, on 2026-11-05, made 15 changes over 5 messages with a
2 s threshold, none refused, at about 100–400 ms each. A change makes the phone vibrate
slightly: pleasant as feedback, tiring on every message, in the owner's words. Plain answers
took roughly 4–7 s.

## Options

### A. A component in the transport, outside the outbox

The agent reports a stage for a message and does no I/O for it. A Telegram `Reactor`, with
one goroutine, turns stages into emoji and timing. A failed call is logged and counted.

### B. Through the outbox

A reaction is one more kind of outgoing message: stored, retried and ordered.

### C. A tool the model calls

The model chooses a reaction, as the owner asked it to on the live try.

## Comparison

| Criterion | A. Outside | B. Outbox | C. A tool |
|---|---|---|---|
| A failure delays a message | Never | It shares a sender, its limits and its backoff | Never |
| A write per reaction | None | A row per change, inside a turn | A journal row |
| Telling the truth | Always: code sets it | Always | Only if the model says so |
| Cost | None | A little disk | A model's tokens and a step |
| A stale reaction after a crash | Possible, says little | A late retry can show one long after | Possible |
| Keeps to "model as little as possible" | Yes | Yes | No |

## Decision

**A.** A reaction is a hint. It is worth nothing late and nothing twice, so it needs no
retries, no ordering and no rows.

| Reaction | When | Delay |
|---|---|---|
| none | An answer inside the threshold | — |
| 🤔 | The model works, past the threshold | The threshold |
| 👨‍💻 | A tool ran or runs; stays to the end of the turn | The threshold, then the dwell |
| 👀 | The message waits behind a running turn | The threshold |
| 👌 | An addition was taken | None: it is the only way to know it was noticed |
| none | The reaction comes off at the end | After a linger |

- **Against flicker:** no change within a dwell of the last, except taking it off and setting
  👌; never back from 👨‍💻 to 🤔 in a turn.
- **Starting values:** threshold 8 s, dwell 3 s, linger 800 ms, as
  `GANGLION_REACTION_AFTER` and `GANGLION_REACTION_DWELL` beside the node's other settings;
  a threshold of 0 turns it off. **The 8 s was never tried on a phone.** The owner settles it
  in the live check, and the value here changes without a new ADR.
- **Refusals.** A refused reaction is not tried again for that message. A `429` pauses all
  reactions for its `retry_after`, and the sender of messages goes on by itself.
- **Bounded.** One entry per message; it goes when cleared, and one older than 15 minutes is
  dropped, so a crash in a turn leaks nothing.
- **A crash** can leave a stale reaction on an old message. Accepted: a rerun clears its own.

Beside this, "typing…" also shows for a turn resumed by a press, which today shows nothing.

## Security

- **Trust boundaries.** The agent's stages cross to the transport inside the process. The
  outgoing call goes to Telegram with the existing client, its timeout and its body cap.
- **What an attacker controls.** Whether they send a message, and from where.
- **Abuse cases, all stopped by code:**
  - **A reaction tells a stranger that the node is alive.** `Noting` wraps `q.Put` behind the
    access list, so a message the list drops reports nothing. A test asserts it. The throwaway
    build set 👀 before the list, and this rule exists because of it.
  - **A flood grows the map.** The entries are bounded as above, and a message that was
    never let in has no entry.
  - **A flood spends the Bot API's limit on reactions.** A `429` pauses reactions alone. A
    quick answer makes no call at all.
  - **A group learns of the bot's work.** Reactions are off where the chat id is negative.
  - **No text is chosen by anyone.** The emoji are a fixed list in code, and a label on the
    metric is an outcome, never a chat or a message.

## Consequences

- A long task is visibly alive, and an addition is visibly heard.
- Quick answers cost no API call and no buzz.
- A goroutine with state in the transport, and a stage hook through the agent on every path.
- A reaction cannot be relied on: nothing may ever depend on one having been shown.

## Revisit when

- The owner finds that it pesters, or says nothing, on the phone.
- Groups need it, with their own allowed reactions.
- Another transport wants stages: the stage type is channel-neutral for that reason.
