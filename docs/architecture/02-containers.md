# Level 2 — Containers and deployment

ganglion is one binary; `--roles` decides which roles a process hosts
([ADR-0004](../adr/0004-one-binary-with-roles.md)). A "container" here is a
role or a data store, not a Docker container.

## Roles and stores

```mermaid
flowchart LR
    tg["Telegram"]
    mqttdev["MQTT devices<br/>P2"]

    subgraph corerole["core role"]
        corep["Core<br/>agent loop, router, policies,<br/>scheduler, tool registry"]
        sqlite[("SQLite<br/>inbox, outbox, history,<br/>turns, cron_runs, allowed")]
        memrepo[("Git repo<br/>memory/*.md, skills")]
    end

    subgraph edgerole["edge role"]
        runner["Runner<br/>wazero, supervisor,<br/>policies, scheduler"]
        mods["WASM modules<br/>deye-reader, tuya-plug,<br/>load-shedding"]
    end

    subgraph workerrole["worker role"]
        api["API provider client<br/>OpenAI-compatible, Ollama"]
        cli["claude CLI worker"]
    end

    bus{{"Bus<br/>P1: in-process<br/>P2: NATS leaf + JetStream,<br/>KV, Object Store"}}

    tg <--> corep
    corep <--> sqlite
    corep <--> memrepo
    corep <--> bus
    runner <--> bus
    runner --> mods
    api <--> bus
    cli <--> bus
    mqttdev -.-> bus

    classDef planned stroke-dasharray: 5 5
    class mqttdev planned
```

| Container | Holds | Technology | Phase |
|---|---|---|---|
| Core | Agent loop, router, policies, scheduler, tool registry, Telegram transport | Go | P1 |
| SQLite | Inbox/outbox, conversation history, `turns`, `cron_runs`, `allowed` | modernc.org/sqlite, WAL | P1 |
| Memory repo | `memory/*.md`, skills; later embeddings | git | P1 (files), P2 (embeddings) |
| Runner | Module runtime, local policies, offline scheduler | wazero, embedded NATS in P2 | P0 (spike), P1 |
| WASM modules | Compiled reflexes with manifests | TinyGo / Rust / AssemblyScript | P0 (Deye by hand), P3 (by the model) |
| API provider client | Chat completions | net/http | P1 |
| claude CLI worker | Standalone heavy tasks | unmodified `claude -p` | P1 |
| Bus | Messages between roles; across nodes buffering, KV, object store | in-process (P1), NATS (P2) | P1 / P2 |

## Deployment: P1, all-in-one

One process on a Raspberry Pi or a small cloud instance. The bus is
in-process; there is no NATS yet.

```mermaid
flowchart TB
    subgraph host["one host: Pi or cloud instance"]
        subgraph proc["ganglion --roles core,edge,worker"]
            c["core"]
            e["edge runner"]
            w["worker: API client"]
        end
        db[("SQLite file")]
        repo[("memory git repo")]
    end
    pc["Owner's PC<br/>claude CLI worker"]
    c --- e
    c --- w
    c --- db
    c --- repo
    c -. "task queue" .- pc
```

## Deployment: P2, core + edges

The core runs a NATS server with JetStream. Each edge runs a leaf node that
dials out to the core, so no inbound ports or NAT forwarding are needed.
While the link is down, edges keep running on their modules and local
policies; events queue up in JetStream and are delivered later.

```mermaid
flowchart LR
    subgraph server["server or cloud"]
        cn["ganglion --roles core<br/>+ NATS server, JetStream"]
    end
    subgraph home["home"]
        e1["ganglion --roles edge<br/>Pi next to the inverter<br/>+ NATS leaf node"]
        esp["ESP32, relays<br/>MQTT"]
    end
    subgraph garage["garage, later"]
        e2["ganglion --roles edge<br/>+ NATS leaf node"]
    end
    subgraph pc["owner's PC"]
        wk["ganglion --roles worker<br/>claude CLI"]
    end
    e1 -- "leaf connection, TLS" --> cn
    e2 -.-> cn
    wk -- "NATS client" --> cn
    esp -- "MQTT" --> e1

    classDef planned stroke-dasharray: 5 5
    class e2 planned
```

An edge needs three files: the binary, a NATS credentials file and the
owner's public key for verifying modules.
