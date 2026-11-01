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

If the process dies before `Complete` commits, the lease expires and the event is
processed again. If it dies between sending and marking, one duplicate
reply is possible (R-DLV-5).

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

## Load-shedding flow (P1)

The first real flow (spec section 8): staged switching of Tuya plugs by
battery state of charge during a power cut.

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
        each plug switches at most once per 15 min,
        kill switch per plug and a global one.
    end note
```

Escalation to the model: Tuya or Deye auth error, a plug not responding, no
data for over an hour. The first week runs in shadow mode.

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
