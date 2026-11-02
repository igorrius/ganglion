# ADR-0032: Answer confirmations with one-time buttons and resume the conversation with a new event

- **Status:** Proposed
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.6](../spec.md#76-action-policies-and-risk-classes) (R-POL-2, R-POL-3, R-POL-4), [7.1](../spec.md#71-guaranteed-delivery) (R-DLV-1, R-DLV-6); design section 5.2 ([M3b and M3c design](../plans/p1-m3b-m3c-design.md#52-confirmations-and-quick-actions-on-buttons)); [ADR-0014](0014-state-cells.md), [ADR-0017](0017-extension-points.md), [ADR-0020](0020-neutral-history-as-conversation-state.md), [ADR-0031](0031-agent-security-baseline.md)

## Context

The policy (ADR-0031) sends side effects, sends to other chats and
unknown `web.fetch` addresses for the owner's confirmation. Until now the
node had no way to ask: every such call failed (`policy.NoAsk`). The
owner answers in Telegram, with a button under a question.

Facts that shape the design:

- **A turn cannot wait.** The turn timeout is minutes and holds a lease
  on the conversation; the answer can take longer (`ttl` 15 minutes by
  default, R-POL-2), and the press itself is an update that queues
  behind the waiting turn (one turn at a time per conversation, R-AGT-1).
- **Delivery is at least once.** A turn can run twice after a crash
  (R-DLV-3); Telegram can deliver an update twice; a person can press
  twice, or press both buttons. The call must run once, and its side
  effects keep the call's key (R-DLV-6).
- **History must stay valid** for OpenAI-compatible APIs (ADR-0020): a
  `tool` message only after the assistant message with its call, and
  every call answered before the next user message.
- **Callback data is public.** Telegram's `callback_data` is at most 64
  bytes and travels through the client; a modified client can send any
  data for any message of a chat it is in.
- **Abuse cases** of design 5.2:
  1. a forged or replayed button runs a call with other arguments;
  2. someone else in a group, or a stranger, presses the owner's button;
  3. a press long after the question still runs the call;
  4. a double press (or Approve then Deny) runs the call twice;
  5. "Run now" on a task with side effects skips the confirmation.

## Options

### A. Fire-and-forget

The press runs the stored call and edits the message ("done"); the model
never hears of it. The turn that asked ends with "awaiting confirmation".

### B. Resume as a new event

The turn ends with the call parked and a placeholder result. The press
(or the expiry) is a new event in the same conversation: it runs or
declines the stored call and starts a model turn whose new message is
the call's outcome, so the model continues ("here is the page you asked
me to read").

### C. Blocking

The turn waits for the press inside the tool call, with the turn timeout
raised to the confirmation `ttl`.

## Comparison

| Criterion | A. Fire-and-forget | B. Resume as a new event | C. Blocking |
|---|---|---|---|
| The model sees the result (a page it asked to read) | no | yes | yes |
| Fits one turn per conversation | yes | yes: the press queues behind nothing | no: the press waits behind the turn that waits for it, unless presses bypass the queue |
| Survives a restart while waiting | yes | yes: all state is in SQLite | no: the waiting turn is lost and reruns, asking again |
| Holds a lease and a worker for minutes | no | no | yes, per question |
| Delivery and idempotency | inbox key per update | inbox key per update, press marked once per event | a second path outside the inbox |
| Effort | small | medium: a resume stage, a pending store, an expirer | small code, large consequences |

## Decision

**B, resume as a new event.** It is the only option where the model
gets the outcome without holding a turn open, and it reuses the inbox,
the turn loop and the outbox, so every delivery guarantee already built
applies to the press.

How it works:

- The policy's confirm branch calls `Asker.Ask` (the `tool/pending`
  store) and returns the question in `Result.Out`; the agent sends it
  with the turn's replies (IdemKey `<call key>:confirm`) and keeps the
  pending id in `State.Pending`. The call's tool message is the
  placeholder "awaiting confirmation". `Ask` is idempotent by the call
  key, so a rerun turn parks the same call.
- `tool/pending` stores the call (its turn id and number, tool,
  arguments, the redacted question), a request cell
  `request.call.<id>` (ADR-0014; lower case, like every cell id) and two
  buttons. A button id is 26
  characters of Crockford base32 from 16 random bytes; callback data is
  `c:<id>`.
- The Telegram transport asks for `callback_query` updates, answers each
  at once (`answerCallbackQuery`, even a refused one), and turns a press
  of `c:<id>` into an event `decision` with the update's key
  `telegram:<bot>:<update_id>`. The access list drops a stranger's press
  like a stranger's message.
- The agent's first stage handles `decision`: `Press` marks the button
  used in one immediate transaction, checks the presser and the chat,
  moves the call from waiting to approved or denied and spends the other
  button. Approve runs the stored call through the journal and the
  registry, not the policy (it was decided), with the original key;
  Deny records "the owner declined". The whole outcome (the result,
  whether it is outside content, the messages the call produced) is
  stored, so a rerun of the turn gives back the same; the question
  edited ("Approved." / "Declined."), and the model runs on a new
  user-role note `[confirmation] <tool>: <result>` (outside content
  wrapped as data and the session tainted). No tool message appears
  without its call.
- A node component, the expirer, marks unanswered calls expired every
  30 s and puts an event `tool.result` for each (key
  `pending:<id>:expired`); the conversation resumes with "the owner did
  not answer in time", if the call is still in its session.
- Quick-action buttons (Delete, Details; later tasks) are stored the same
  way with an action name; a press runs the registered action, edits the
  message and starts no model turn. A refused press on one removes only
  its buttons (an `Edit` without text, `editMessageReplyMarkup`), so the
  list entry keeps its text.

The abuse cases:

1. **Forged or replayed button:** the data is only a random one-time id;
   the tool and its arguments are in SQLite, fixed when the button was
   made. An unknown id does nothing.
2. **Someone else presses:** the access list drops strangers; `Press`
   accepts only the principal the button was made for, in the chat it
   was sent to (`ErrNotYours`: nothing happens, nothing is edited).
3. **Late press:** a button expires with its question; a late press
   edits the message to "Expired." and runs nothing.
4. **Double press:** the inbox drops a redelivered update by its key;
   two different updates race on `Press`, which lets one through and
   answers the other "Already handled."; a rerun of the same event gets
   the same press back and finds the outcome stored, so the call runs
   once. A crash between running the call and storing its outcome reruns
   it with the same key, which the tool's own idempotency covers
   (R-DLV-6).
5. **"Run now" on a task with side effects:** the quick action asks
   through the pending store (`Ask`, the button as the call's turn) and
   shows the question in place of the job's message; the approved call
   (`schedule.run_now`) runs like any confirmed call (task 6).

## Consequences

- The model continues after a confirmation; a page it asked to fetch is
  read in the next turn, with the session tainted.
- Every press and expiry is an inbox event: ordered within its
  conversation, retried, given up with an apology like any message.
- The question message can only be edited from a press, which carries
  its message id; the expiry does not know it, so an expired question
  keeps its buttons until someone presses one ("Expired.").
- A press is honoured even after `/new`: the call runs and its question
  is edited. The model hears of the outcome only while the call is in
  the session's pending list, so an answer after `/new`, or to a
  question a quick action asked, starts no model turn; an expiry of a
  call whose session moved on is recorded the same way.
- Pending ids that no longer wait are dropped from the state at the
  start of every message turn and on `/resume`.
- An `Ask` that half-fails (the call stored, its request cell not) fails
  the call: the model gets the error and no question is sent. The stored
  call waits unseen and expires silently, leaving a journal entry.
- The confirmed call's arguments sit in SQLite until the call is done,
  like the history that holds them anyway; the question shown is
  redacted.
- A resumed turn does not count against the daily turn limit stage; the
  router's budget still applies.

## Revisit when

- a transport without editable messages or buttons needs confirmations
  (a text reply "yes <code>" instead);
- the edge executes actions (P2): the pending store and the press move
  to the node that acts (R-POL-1);
- a call needs a confirmation by someone other than the person who asked
  (family members, M7).
