# Level 3 — Components

What is inside each role. Package names are in
[05-code-map.md](05-code-map.md).

## Core

```mermaid
flowchart TB
    subgraph transports["Transports"]
        tgt["Telegram<br/>long polling"]
        mqt["MQTT<br/>P2"]
    end

    acl["Access list<br/>strangers dropped, no reply"]
    inbox[("Inbox<br/>idemKey unique")]
    actors["Turn workers<br/>parallel; one turn per conversation at a time;<br/>sessions"]
    loop["Agent loop<br/>model, tools, next step"]
    history["History<br/>neutral format, summaries"]
    router["Router<br/>filters, rules, weights, fallback"]
    providers["Providers<br/>OpenAI-compatible, claude CLI"]
    tools["Tool registry<br/>memory.*, schedule.*,<br/>message.send, device.*"]
    policies["Policies<br/>risk classes, confirmations, audit"]
    memory["Memory<br/>memory/*.md in git"]
    sched["Scheduler<br/>crons, catch-up;<br/>reminders, tasks with grants"]
    outbox[("Outbox")]
    sender["Outbox sender<br/>retries, 429 retry_after"]
    turns[("turns<br/>trace, cost")]

    tgt --> acl
    acl --> inbox
    mqt -.-> inbox
    sched --> inbox
    inbox --> actors
    actors --> loop
    loop <--> history
    loop --> router
    router --> providers
    loop --> policies
    policies --> tools
    tools <--> memory
    tools --> sched
    loop --> outbox
    loop --> turns
    outbox --> sender
    sender --> tgt

    classDef planned stroke-dasharray: 5 5
    class mqt planned
```

| Component | Responsibility | Requirements |
|---|---|---|
| Transports | Turn channel input into `Event`s; send `Outgoing` | R-DLV-2, R-DLV-8 |
| Inbox / outbox | Durable at-least-once delivery, deduplication by `idemKey`, transactional replies | R-DLV-1…7 |
| Conversation actors | One turn at a time per conversation, leases, timeouts | R-AGT-1, R-DLV-3 |
| Agent loop | Model call → tool calls → next step, step limit, depth limit | R-AGT-3, R-AGT-4 |
| History | Provider-neutral transcript, compaction | R-AGT-2 |
| Router | Ordered candidates by skill requirements, budget, health | R-RTR-1…3 |
| Providers | Talk to model backends, translate MCP tools | R-PRV-1…4 |
| Policies | Decide autonomous / confirm / forbid; audit | R-POL-1…5 |
| Tool registry | Tools available to the model, local and on edges | — |
| Memory | Facts and skills in git, nightly consolidation | R-MEM-1…4 |
| Scheduler | Crons in the node's zone with catch-up after downtime; reminders and agent tasks with grants, each task in its own conversation | R-SCH-1, R-SCH-2, R-POL-8 |
| turns | Trace and cost of every turn | R-AGT-5 |

## Edge runner

```mermaid
flowchart TB
    trig["Triggers<br/>cron, tool call, event"]
    sched["Offline scheduler"]
    sup["Supervisor<br/>start, limits, restart,<br/>blue/green, crashloop"]
    wz["wazero runtime<br/>memory and time limits"]
    mod["WASM module<br/>handle; init, health in P1"]
    host["Host functions<br/>log, state, http.fetch, event.emit,<br/>tool.call, secret.get, config.get, now"]
    pol["Local policies<br/>kill switches, ttl, risk class"]
    kv[("Module state<br/>KV")]
    mcp["MCP subprocess host<br/>non-WASM modules, P3"]
    bus{{"Bus to core"}}
    dev["Devices and vendor APIs"]

    trig --> sched
    sched --> sup
    sup --> wz
    wz --> mod
    mod --> host
    host --> pol
    pol --> dev
    host <--> kv
    host -- "event.emit, escalation" --> bus
    bus -- "tool.call, deploy, policy.update" --> sup
    sup -.-> mcp

    classDef planned stroke-dasharray: 5 5
    class mcp planned
```

A module sees only the host functions its manifest grants; by default it
gets nothing (R-RUN-1). Every host call that changes the world passes the
local policy check (R-POL-1).

## Worker

```mermaid
flowchart LR
    q{{"Task queue<br/>bus"}}
    w["Worker loop<br/>health, limits, budget"]
    api["OpenAI-compatible client"]
    cli["claude -p<br/>unmodified CLI"]
    tc["Toolchain<br/>TinyGo, Rust; P3 module builds"]
    q --> w
    w --> api
    w --> cli
    w -.-> tc

    classDef planned stroke-dasharray: 5 5
    class tc planned
```

Workers report health and exhausted limits so the router can skip them
(router layer 2).
