# Live turns design: additions, status reactions, a visible plan

Status: approved by the owner on 2026-11-05 and built. It feeds the spec changes,
the ADRs and the step-by-step plan (`p1-live-turns.md`). It is not itself a plan.

Marks, as in the [M3d design](p1-m3d-host-commands-design.md): **(owner)** the
owner decided it; **(call)** the author's call, open to correction; **(builder)**
left to whoever implements it.

## 1. Why

The owner sends a task and, while the bot works on it, thinks of more and writes
it as a second message. They mean one thought, widened. Today the bot does not
hear that: each message is its own turn, in order
([R-AGT-1](../spec.md#72-conversations-history-and-the-agent-loop)). The first turn
runs to its end, its answer may already be out of date, and the second turn then
spends another model call. The second message gets no sign that it was seen.

In the owner's words, a person would take each next message as enriching the
context of the request. They also want to see what is happening with a task, and
they liked the plan with ticks they saw in OpenClaw.

**What success looks like:** the owner adds to a request mid-work and gets a final
answer that takes everything into account, loses nothing the model already wrote,
knows the addition was noticed, and is not pestered while it happens.

## 2. Goals, non-goals, anti-goals

**Goals, in the order they are built, each usable alone:**

1. **Additions:** a message written while the agent works joins the running turn
   at the next boundary.
2. **Status reactions:** a reaction on the person's message shows work that takes
   long, and that an addition was taken.
3. **A visible plan:** for work with several steps, a message with the steps,
   edited as they go. M3d later adds its one-press approval.

**Non-goals, written to the spec's "Not now" list:**

- `/stop` and interrupting a run. The owner's case is adding, not stopping. An
  automatic interrupt is rejected outright: what a tool already did cannot be
  undone. (call)
- A reaction chosen by the model. On the live try the owner asked the model to
  set one, it said it had no such tool, and the owner found that a nice idea for
  later, "if it is really needed". (owner)
- A quiet window before a turn starts. (owner, section 3)
- Status on each line of a plan by reactions: Telegram puts a reaction on a whole
  message, so plan lines use marks in the text.
- Parallel tool calls in the API route. The plan's lines must not stand in the
  way of them later.

**Anti-goals, failure even if it technically works:**

- **It pesters.** Flicker, or the phone buzzing on every message. (owner)
- **A status said twice.** A reaction meaning "done" beside the answer that says
  so. (owner)
- **An addition with more authority than a message.** A new way in must not
  loosen a policy, a taint rule or the access list.
- **A message lost or answered twice after `kill -9`.** M2's guarantee stays.

## 3. Part 1: additions

### 3.1 What the owner decided

- The addition goes in **between tools**, not after a wait for silence. The owner
  rejected the quiet window: their additions come well after the first message,
  while it is being worked on, and waiting would only be slow. (owner)
- When the model has just written an answer with no tool to follow, and an
  addition is already waiting, **that answer is not thrown away**. It is the answer
  to the first question, and it is sent as a quote; one more model call then answers
  with the addition in view. Nothing the model wrote is hidden from the person, so
  there is no risk of a rewrite silently dropping something. (owner, replacing the
  earlier idea of holding the answer and writing one combined answer)

### 3.2 Boundaries

A model does not hear anything while it writes: each call is a snapshot of the
history. So an addition can enter only between calls. At each boundary the agent
asks the queue for additions waiting behind the turn's event (3.4).

| Boundary | API route (`agent.steps`) | Claude CLI route |
|---|---|---|
| A tool finished, more tools were planned in the same model message | Remaining calls are skipped, each with a paired result saying it did not run because a new message came; the model decides again. A running tool is never interrupted | The core sees the call return over the bridge. See 3.3 |
| Before the next model call | The additions are appended as user messages | Same: the next `claude -p` receives them in its history |
| The model answered with no tool call | The answer goes out as a quote (3.5) | Same |

Skipped tool results follow the API's pairing rule, and OpenClaw's: every call
keeps a result, so the history stays valid.

**The check runs before every tool call, the first one included.** An addition
already waiting when the model asks for a tool may say "don't touch X", and a waiting
forward has not tainted the session yet. So no call starts while an addition waits:
on the API route all its calls are skipped; on the CLI route `Exec` refuses the call
and the run ends. This is stricter than OpenClaw, which lets the first call start. A
stream of additions could keep tools from running, but only until `MaxSteps` ends the
turn. (call)

### 3.3 The CLI route

The route to Sonnet is the claude CLI, whose tool loop runs inside the CLI. Every
call still passes through the core (ADR-0036), which is the boundary we see.

**Decision (call):** when a call returns and an addition is waiting, the core ends
that CLI run and starts a new one with the whole history plus the addition. The
core already sends the whole history on each call and logs the calls a run made
(`takeExecLog`), so nothing is lost, and both routes run the same loop.

Rejected: writing into the running CLI's stdin (`--input-format stream-json`). The
flag exists, but the
[headless docs](https://code.claude.com/docs/en/headless) do not say when a message
sent mid-run is picked up. Relying on it would tie a guarantee to behaviour nobody
promised. An ADR compares the two with a small test of the stdin route.

Also rejected: appending the addition to a tool's result. Results are data, marked
as outside content (ADR-0031); an owner's words must not arrive as data, and
outside text must never look like an owner's words.

What this has to get right:

- **Not a failure.** `Agent.call` reads a CLI run that ended after running tools
  as a failure with no fallback (`OutcomeError`, ADR-0036). An ended-for-an-addition
  run needs its own result, which is not a failure, not a fallback and not a retry.
  (builder: a sentinel error or a field on the response)
- **Cost, measured first.** A new run re-sends the context. A small measurement comes **before** Part 1 is built, since a bad number
  can change this decision (section 7). **The bar (owner):** ending the run stays if a
  turn with an addition costs at most twice what the two separate turns would cost
  together. Above that, another way is found, starting with the stdin route.
- **Tokens of an ended run.** With `--output-format json`, usage arrives only in the
  final result, so a killed run reports none, and the daily limit would undercount.
  The core counts an estimate from the size of what it sent, erring high; moving the
  main route to `stream-json` is a larger change, not worth it for this. The ADR states
  the estimate. (call)
- **Timing.** End the run right after the result is written back, before the model's
  next call can begin where possible. Losing the race costs part of a call, not
  correctness.

### 3.4 What counts as an addition

The agent takes the **unbroken run** of pending events directly behind the turn's
event, in order, stopping at the first that does not qualify. Later messages may
never overtake an earlier one (R-AGT-1). An event qualifies when all hold (call):

- the running turn was started by a person's message, not by a schedule, a press
  or an expiry;
- its kind is a message, in the same conversation, **from the same person**;
- it is not a command (the agent's command table decides);
- it has not expired, and the sender is within the daily limits.

A message from someone else in a group, a command, or a press waits for its own
turn, as today. The access list already filters at `Put`, so a stranger's message
is never in the inbox.

**An addition is a message like any other:**

- it goes through `userContent`, with the speaker's line and outside text wrapped;
- a forward or an outside reply taints the session **before the next model call**,
  as `agent.answer` does at the start of a turn (ruling R35);
- the admin flag is the same, since the sender is the same;
- files are described the same way (R-DLV-10);
- a local-only session stays local-only.

### 3.5 The answer that came before the addition

The model's answer to the first question is real: it goes to the person and stays in
the history as written, since the person saw it, so a later "as I said" is true. The
next call has the answer and the addition in view, and need not repeat what is still
right. (owner)

- **A collapsed quote (owner).** In Telegram the whole earlier answer goes out as an
  `expandable_blockquote`, whatever its length, so it takes little room, opens on a tap,
  and the answer to look at is the next message. The owner compared it with a spoiler on
  the test bot and chose this. The renderer already makes such quotes
  (`transport/telegram/markdown.go`), but only above 500 characters and only for quotes
  in the Markdown. Telegram does not nest quotes, so quotes inside the answer are
  flattened. The look follows what each messenger can do: a transport with no quotes
  sends plain text. Other messengers are not built yet; the
  agent marks the message as superseded and each transport decides how to show it.
  (builder: the field on `event.Outgoing`)
- **Sent at once (call).** It goes through `Outbox.Enqueue`, as a plan does (section 5),
  with a key from the turn, so a rerun does not send it twice. The person reads the
  first answer while the second is being written.
- **Text written alongside tool calls (call).** On the API route the model's text next
  to its tool calls is in the response, and if a skip drops those calls, the text goes
  out as a quote too. On the CLI route `--output-format json` gives text only in the
  final result, so **a run ended for an addition loses what it wrote in that run**. Only
  its tool calls survive. Reading it would need `stream-json` (see Tokens in 3.3). The
  ADR states this, and the next call still has every tool result.

**The budget holds it:** additions and further answers spend the turn's `MaxSteps`. When
it runs out, the newest answer is sent as the normal reply, and whatever else is waiting
becomes the next turn. A person who keeps writing cannot keep the bot answering forever.
(call)

**The clock is not extended.** New runs and rewrites spend the same turn timeout, and
it stays below the lease. Once the time is short, no more additions are taken: they
wait for the next turn. (call; builder: how much time counts as short)

**A turn that fails after taking additions** behaves as a failed turn does today: one
failure reply, the person's messages stay in the history, and every taken event is
marked done with it. They are not released to run as turns of their own, since the
person has seen 👌 and now reads that it failed, and can send it again. Nothing is
silently lost: the reply says so. (call)

### 3.6 Queue

`queue.Queue` gains a read of the qualifying candidates behind a lease, and
`Complete` also marks the taken events done, **in its one transaction** (R-DLV-4).
(builder: the signature)

After a crash before `Complete`, nothing was marked: the turn restarts from its
first message and takes the additions again. The existing rule stands that a turn
reruns under the same call keys; because a rerun may take additions at different
moments, the same call number can be a different call. The builder checks this
against how `tool.Call.Key()` is used and says in the ADR whether it needs a change.

Polling the inbox at each boundary is enough: it is a local SQLite read and
boundaries are seconds apart.

### 3.7 The answer

The turn's final answer is a reply to the newest message taken (call), and any earlier
answer has already gone out as a quote (3.5). The turn's record lists all the events it
took; `make crash-agent` counts each message answered once, with the
taken ones counted through that record.

## 4. Part 2: status reactions

### 4.1 What the live try showed

A throwaway build ran on @test_bot on 2026-11-05 (not kept; the patch stayed
outside the repository until this design is settled):

- With a 2 s threshold: 15 reaction changes over 5 messages, none refused by
  Telegram, about 100–400 ms each.
- Plain answers took roughly 4–7 s, estimated from the log.
- On the phone, in the hand, a change gives a small light vibration. The owner
  found it pleasant as feedback, and tiring if it came on every message.
- **The 8 s threshold has not been tried on a phone.** The owner agreed with the
  idea; no message went through that run. It is a starting value, and the live
  check in section 7 settles it.

### 4.2 The rule

Telegram gives a bot one reaction per message, from a
[fixed list](https://core.telegram.org/bots/api#reactiontypeemoji); a new one replaces
the old. A reaction means **"this is taking long"**, not "I am working": "typing…"
already says that, always. (owner: only when the answer is not quick; no status
said twice)

| Reaction | When | Delay |
|---|---|---|
| none | An answer inside the threshold | — |
| 🤔 | The model is working, past the threshold | threshold (8 s to start) |
| 👨‍💻 | A tool ran or runs, past the threshold; stays to the end of the turn | threshold, then the dwell |
| 👀 | The message waits behind a running turn, past the threshold | threshold |
| 👌 | An addition was taken | **none**: it is the only way to know it was noticed |
| none | The bot answered or asked: the reaction comes off | at the end |

Dropped after the try: 🫡 and 🤷, since the answer or the error says so itself, and
🙏, since the question with its button says so. (owner's reasoning; 🙏 applied by the
author, owner agreed)

**Against flicker:** no change within a dwell (3 s to start) of the last, except
taking the reaction off at the end and setting 👌; never back from 👨‍💻 to 🤔 in a
turn. (call)

### 4.3 Shape

- **Who sets it:** code, never the model: free and always true.
- **Agent and transport stay apart.** The agent reports a stage (thinking, tool,
  addition taken, done) for a message through a small channel-neutral interface; the
  Telegram transport turns stages into emoji and timing. Other transports do nothing.
  This extends the existing `core.TurnHook`, which ends a turn's "typing…". (builder)
- **Not in the outbox.** A reaction is a hint: no retries, no ordering, no rows. A
  failed call is logged and counted; a `429` pauses reactions and never delays a
  message. (call)
- **Only allowed senders.** The throwaway set 👀 from `messageEvent`, before the
  access list. Real code must act after the filter, or a stranger learns the bot is
  alive, against the rule that strangers get no reply.
- **Taking it off:** after the reply is delivered, or a short linger when there is no
  reply to wait for. (builder: the outbox can report it, or the linger of 800 ms from
  the try)
- **Private chats first.** Groups have their own allowed reactions, and a reaction
  there is seen by everyone. (call)
- **A crash** can leave a stale reaction on an old message. Accepted: a rerun's turn
  clears its own, and it says nothing false for long.
- **Settings:** threshold, dwell and on/off in the node's configuration. (call)
- **A pause after a press:** in a resumed turn neither "typing…" nor a reaction shows
  today. Show "typing…" for a resumed turn; a small fix. (call)

Replaces the "reaction on a message the agent has seen" idea in "Not now" for private
chats; for groups it stays there.

## 5. Part 3: a visible plan

The [M3d design](p1-m3d-host-commands-design.md) (section 7.1) already holds the
owner's plan message: numbered steps in plain words, one message edited as work goes
(⏳ ✅ ❌ ⏭), the outcome at the end, kept in the chat. Tools `plan.show` and
`plan.step`; a directive in the prompt asks for a plan; nothing forces one for
read-only work.

This milestone builds **that display, without the approval**. M3d adds its press
(7.2, ADR-0040) on top, and its task 3.24 splits accordingly. (owner: wants the plan
shown in the chat; call: moving the display ahead of M3d, and the order of the three
parts)

Two things that design did not settle:

- **A plan must appear mid-turn.** A tool's messages today go out at `Complete`, at the
  end of the turn, so a plan would arrive with the answer, ticked to the end. A plan
  uses `Outbox.Enqueue` (it exists, "outside a turn") with a key from the call
  (`c.Key()`), so a rerun does not post a second plan and `SentRef` finds the message
  to edit. This is a small, stated exception to "replies go out with the turn"
  (R-DLV-4): a plan is progress, not the reply. An ADR records it.
- **Plans and additions.** After an addition the model may call `plan.show` again; the
  same message changes rather than a new one being posted. (call)

Also settled here, all (call):

- **Budget.** On the CLI route a turn may make `MaxSteps` tool calls, 8 by default, so
  ticking a four-step plan would use them all. Plan tools do not count against the cap.
  `plan.step` marks a step and starts the next in one call, and the prompt asks the
  model to send it beside the next real call, in the same model message, so a tick costs
  no round trip of its own. A count of plan calls per turn still has its own small
  ceiling.
- **A plan belongs to the conversation, not the turn.** A press starts a new event with
  a new turn id, and the plan must survive that (M3d needs this too). One active plan per
  conversation, in a small table of its own, written as the plan is shown, since state
  written at `Complete` would be too late. A second `plan.show` changes the same message.
- **Identity across a rerun.** The plan's table row, not the call key, finds the message:
  a rerun's `plan.show` finds the active plan and edits it. The ticks follow what the
  rerun reports.
- **Ending early.** When a turn fails or hits its limit with lines unfinished, code marks
  them ⏭ and leaves the message in place; a parked call leaves them as they are, waiting
  for the press. The model writes the outcome line in its last `plan.step`; with none, the
  plan ends with no outcome line.

**Throttling:** edits coalesce to stay inside Telegram's limits and to keep the plan
from flickering, the same anti-goal as reactions. (builder: the rate)

Parallel calls in the future fit: each line changes on its own.

## 6. Security

A pass from the attacker's side (spec principle 9):

| Abuse | Answer |
|---|---|
| A page's injection tries to reach the running turn as an "addition" | Additions come only from inbox events of the same person. Tool results never become additions |
| A forward arrives mid-turn, hoping to run with a clean session's rights | It taints before the next call (3.4). A tool already running keeps the decision it had, which was right for what was known |
| Someone in a group adds to the owner's running turn, borrowing the admin's rights | Only the same person qualifies (3.4) |
| A message flood keeps a turn alive and burns tokens | `MaxSteps` caps it, and the daily limits count (3.5). The flood becomes turns, as it does today |
| An addition slips past an approved call | Policies run per call as now. A press approves one parked call; an addition grants nothing |
| Reactions show a stranger that the node is alive | Only after the access list (4.3) |
| A plan reads as approval before M3d | No button, and no tool checks it. It displays; policies decide |
| Plan text carries an injection into the chat | It is model text and goes out as replies do, rendered as entities, never as markup the model controls (ADR-0025) |

## 7. How we know it works

- **No model, in every `make test`:** the scripted model and the fake Bot API (it needs
  `setMessageReaction`). An addition enters at each boundary; a skipped call has a
  paired result; an earlier answer goes out once as a quote and stays in the history; qualification stops at a command
  and at another person; `MaxSteps` ends the rewriting; a forward taints before the next
  call; order holds.
- **Crash:** `make crash-agent` with additions: each message answered once, in order,
  across 200 and 1,000 `kill -9`.
- **Seeded faults** (`make lab-faults`): additions from another person let in; a forward
  not tainting; each turns a scenario red.
- **Chatlab:** only the scenarios this touches, by `make lab`. A full run is never
  automatic (owner).
- **Measured, before Part 1 is built:** tokens and time for a turn with an addition
  against two separate turns, on the CLI route, including whether the prompt cache holds
  across runs. Runs are left to finish where needed, so usage is reported. The same test
  tries the stdin route (3.3). The bar is in 3.3.
- **Metrics, in the same commit (R-OBS-8):** additions taken, runs ended for one, answers
  sent as quotes, reactions set and failed, plan edits.
- **Live, by the owner on the test bot, since chatlab cannot see real Telegram:** does it
  pester; is the 8 s threshold right; does an addition feel heard; does a plan read well.

## 8. What changes in the documents

- Spec first: R-AGT-1 and R-DLV-4 gain the addition and the plan's exception; a new
  requirement group; the items of section 2 in "Not now"; the phase plan gains this
  milestone before M3d, and task 3.24 splits. Its name is the builder's (call: M3e would
  mislead, as it comes before M3d).
- ADRs, Proposed until the owner accepts them, each comparing at least two options:
  - additions at boundaries, with the CLI route (3.3) and the queue contract (3.6);
  - status reactions outside the outbox (4.3);
  - the plan's mid-turn delivery (5).
- `docs/architecture/05-code-map.md` and the matching level page, in the same commits.

## 9. Left to the builder

Signatures, the stage interface's name and place, the rate of edits, how an ended CLI
run reports itself, and the prompt's wording that an addition belongs to the same request.
