# Level 4 — Runtime flows

How the components cooperate in the scenarios that matter most.

## Chat turn with guaranteed delivery (P1)

```mermaid
sequenceDiagram
    autonumber
    participant TG as Telegram
    participant T as Telegram transport
    participant Q as Inbox / outbox
    participant A as Conversation actor
    participant R as Router
    participant M as Model provider
    participant P as Policies and tools
    participant S as Outbox sender

    TG->>T: update (update_id)
    T->>Q: Put(event, idemKey = update_id)
    Note over Q: duplicate idemKey is a no-op
    T->>TG: advance offset only after Put
    A->>Q: Lease(10 min)
    Q-->>A: event
    A->>R: Route(task requirements)
    R-->>A: candidates in order
    loop until the model answers or the step limit is hit
        A->>M: Chat(history, tools)
        M-->>A: reply or tool calls
        A->>P: tool call with key turn_id + n
        P-->>A: result, or "needs confirmation"
    end
    A->>Q: Complete(reply, session state, processed mark)
    Note over Q: one SQLite transaction
    S->>Q: next unsent reply
    S->>TG: sendMessage, retry on 429
    S->>Q: mark sent
```

If the process dies before `Complete` commits, the restarted core releases
the lease and the turn runs again from its start, under the same call keys
(R-DLV-6). That run does not count as a failed attempt; an event that more
than 10 restarts cut short is given up on
([ADR-0046](../adr/0046-a-restart-is-not-a-failed-attempt.md)). If it dies
between sending and marking, one duplicate reply is possible (R-DLV-5).

## Confirmation on a button (P1)

A message written during a turn joins it at the next boundary
([ADR-0050](../adr/0050-additions-join-a-running-turn.md)): before each model call and each
tool call the agent reads what waits behind the turn's event. No tool call starts while one
waits. An answer it overtook goes out at once, marked as superseded, and `Complete` marks
what the turn took as done in its one transaction.

On the CLI route a call that returns with an addition waiting ends the run: the core cancels it
with a cause of its own and starts a new one with the whole history. That is not a failure,
a fallback or a retry.

```mermaid
sequenceDiagram
    participant P as Person
    participant Q as Inbox and outbox
    participant A as Agent
    participant M as Model
    P->>Q: first message
    Q->>A: lease
    A->>M: call
    P->>Q: addition
    M-->>A: answer
    A->>Q: Behind: the addition qualifies
    A->>Q: Enqueue the answer, superseded
    A->>M: call with the answer and the addition
    M-->>A: final answer
    A->>Q: Complete: reply, state, both events done
```

A call that needs the owner's confirmation does not hold its turn open:
the turn ends with the call parked, and the press is a new event that
resumes the conversation ([ADR-0032](../adr/0032-buttons-and-resume.md)).
The press is acknowledged before its action runs (ruling R40); a press
that becomes no event, or that the access list drops, the transport
answers itself.

```mermaid
sequenceDiagram
    autonumber
    participant TG as Telegram
    participant T as Telegram transport
    participant Q as Inbox / outbox
    participant A as Agent
    participant P as Policy
    participant D as Pending store
    participant X as Tool

    A->>P: tool call (key turn_id + n)
    P->>D: Ask: store call, request cell, two buttons
    P-->>A: "awaiting confirmation" + question with buttons
    A->>Q: Complete(question, reply, Pending += id)
    Q-->>TG: question with buttons c:<id>
    TG->>T: callback_query
    T->>Q: Put(decision, idemKey = update_id, query id)
    A->>Q: Lease
    A->>D: Press (one-time, principal, chat, expiry)
    A->>Q: Enqueue(toast "Approved", edit "⏳ Approved, running…")
    Q-->>TG: answerCallbackQuery(text), editMessageText (no buttons)
    alt approve
        A->>X: stored call, same key, journal "approved", no policy
        X-->>A: result
    else deny
        Note over A: journal "denied", result "the owner declined"
    end
    A->>D: Decide(done, result)
    A->>A: model on "[confirmation] tool: result"
    A->>Q: Complete(the call's messages, edit "Approved, 21:09" with the question folded, answer)
    Note over D,Q: unanswered: the expirer puts tool.result every 30 s, same resume with "expired"; the question closes itself, found by its outbox ref
```

## A long host command (P1)

A host command still running after the short wait (20 s) does not hold its
turn open either (R-CMD-4). The call answers "still running" and the turn
ends with the command's key in the state, which keeps the plan open. The
command goes on under the runner. Its end is put into the conversation's
inbox once as a `command.done` event, by the runner's announce pass every
30 s (not at the moment it ends), and that event resumes the
conversation as a press does: the outcome is outside data, the model has
its tools, and its calls run one level deeper. A command of a scheduled
task resumes as a turn of that task, under its grant (ruling 7 of the M3d
plan). The session may have moved on by then (`/new`, or the idle cut).
Then the conversation gets one fixed line, without a model, and the output
stays out of the new session. A call whose context ends while it waits
(the turn's clock) leaves its command in the background the same way.

```mermaid
sequenceDiagram
    autonumber
    participant P as Person
    participant Q as Inbox / outbox
    participant A as Agent
    participant M as Model
    participant X as host.exec
    participant R as Runner

    P->>Q: "how big is ~/project?"
    Q->>A: lease
    A->>M: call
    M-->>A: host.exec du -sh ~/project
    A->>X: call (key turn_id + n)
    X->>R: Exec: row "running", command in the sandbox
    Note over X,R: 20 s pass, or the call's context ends
    R-->>X: not done: background
    X-->>A: "still running", Job = key
    A->>M: call with the result
    M-->>A: "it runs; I'll tell you"
    A->>Q: Complete(answer, Commands += key)
    Note over R: the command ends; its row is "done", not announced yet
    Note over R: the announce pass, every 30 s
    R->>Q: Put(command.done, idemKey command:<key>:done)
    Q->>A: lease
    A->>R: Get(key): argv, outcome, principal, depth
    alt the session lists the key
        A->>M: "[command finished] du -sh ~/project: <external>…</external>", with tools
        M-->>A: "~/project is 2.1 GiB"
        A->>Q: Complete(answer, Commands -= key, tainted)
    else the session moved on
        A->>Q: Complete("The command … finished after this conversation moved on.")
    end
```

## Module tick and escalation (P0–P1)

The Deye monitor as a reflex: no model in the normal path.

```mermaid
sequenceDiagram
    participant C as Runner scheduler
    participant W as wazero
    participant D as deye-reader module
    participant H as Host functions
    participant API as DeyeCloud
    participant Core as Core
    participant LLM as Model

    C->>W: trigger cron
    W->>D: handle(cron, nil)
    D->>H: secret.get(DEYE_APP_ID)
    D->>H: http.fetch(deyecloud)
    H->>API: HTTPS, allowed domain only
    API-->>H: SOC, grid status
    D->>H: state.put(last sample)
    D->>H: event.emit(battery.changed)
    H->>Core: event, no model involved
    alt auth error or unexpected data
        D->>H: event.emit(module.exception)
        H->>Core: escalation
        Core->>LLM: investigate
        LLM-->>Core: explanation or a new module version
    end
```

## Load-shedding flow (planned, after P1)

Moved out of P1 by [ADR-0058](../adr/0058-load-shedding-leaves-p1.md); kept
as the first flow for the flow engine (spec 9.6): staged shedding of loads by battery
state of charge during a power cut. The flow sets the desired state of load
cells; their backend (`virtual` in P1, Tuya plugs later) makes it real, and a
Telegram notifier observes the changes ([ADR-0014](../adr/0014-state-cells.md)).

```mermaid
stateDiagram-v2
    [*] --> AllOn
    AllOn: All on
    BigOff: Big boiler off, kitchen on
    BothOff: Both boilers off
    AllOn --> BigOff: SOC below 50 percent and grid down
    BigOff --> BothOff: SOC below 40 percent
    BothOff --> BigOff: SOC at least 50 percent or grid back
    BigOff --> AllOn: SOC at least 60 percent or grid back
    note right of BigOff
        In every state: data older than 20 min means no switching,
        a manual switch-on is respected for 2 h,
        each load changes at most once per 15 min,
        kill switch per load and a global one.
    end note
```

Escalation to the model: Deye or backend auth error, a backend not
converging desired and reported state, no data for over an hour. Shadow mode
is simply the `virtual` backend.

## Module rollout via desired state (P2)

```mermaid
sequenceDiagram
    participant Core as Core
    participant OS as NATS Object Store
    participant KV as NATS KV
    participant E as Edge runner
    participant Old as Module v2
    participant New as Module v3

    Core->>OS: put modules/deye-monitor v3 .wasm
    Core->>KV: modules.deye-monitor = version, sha256, signature, manifest
    Core->>KV: nodes.edge-home.desired = v3
    KV-->>E: watch fires
    E->>OS: download v3
    E->>E: verify sha256 and owner signature
    E->>New: init, health
    alt health ok
        E->>New: take over triggers
        E->>Old: drain, then unload
        E->>KV: nodes.edge-home.actual = v3
    else health fails
        E->>New: discard
        E->>KV: nodes.edge-home.actual = v2, error
    end
    Note over Core,KV: rollback means writing v2 back to desired
```

## Escalating a dangerous action (P2–P3)

```mermaid
sequenceDiagram
    participant F as Flow or model
    participant P as Policies on the acting node
    participant Rv as Reviewer model
    participant H as Human in Telegram
    participant Dev as Device

    F->>P: tool.call gate.open, ttl 60 s
    P->>P: kill switch, invariants, time window, rate limit
    alt class comfort
        P->>Dev: act
    else class physical safety
        P->>Rv: does this action fit the policy?
        Rv-->>P: block or pass
        P->>H: is that you?
        alt yes within ttl
            P->>Dev: act and audit
        else no answer or expired
            P->>P: deny and audit
        end
    end
```
