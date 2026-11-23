# Level 2 — Containers and deployment

ganglion is one binary; `ganglion run --roles` decides which roles a process hosts
([ADR-0004](../adr/0004-one-binary-with-roles.md)). A "container" here is a
role or a data store, not a Docker container.

## Roles and stores

```mermaid
flowchart LR
    tg["Telegram"]
    web["The web<br/>public pages and APIs,<br/>Brave or DuckDuckGo search"]
    mqttdev["MQTT devices<br/>P2"]

    subgraph corerole["core role"]
        corep["Core<br/>agent loop, router, policies,<br/>scheduler, tool registry,<br/>html2md module,<br/>host commands in the sandbox"]
        sqlite[("SQLite<br/>inbox, outbox, history, turns,<br/>jobs, cron_runs, tool_calls,<br/>pending calls, allowed")]
        memrepo[("Git repo<br/>memory/*.md, persona/*.md, skills")]
    end

    subgraph edgerole["edge role"]
        runner["Runner<br/>wazero, supervisor,<br/>policies, scheduler"]
        mods["WASM modules<br/>a reader, a reflex"]
        cells[("State cells<br/>desired / reported<br/>P1")]
    end

    subgraph workerrole["worker role"]
        api["API provider client<br/>OpenAI-compatible, Ollama"]
        cli["claude CLI worker"]
    end

    bus{{"Bus<br/>P2 M8: embedded NATS server,<br/>JetStream on disk (or an external one);<br/>M10: an edge is a leaf with its own JetStream domain;<br/>later: Object Store"}}

    tg <--> corep
    tg -- "updates by webhook, when the file says so:<br/>the public listener, secret header (ADR-0078)" --> corep
    corep -- "web.fetch, web.search:<br/>public addresses only (ADR-0035)" --> web
    corep <--> sqlite
    corep <--> memrepo
    corep <--> bus
    runner <--> bus
    runner --> mods
    mods <--> cells
    api <--> bus
    cli <--> bus
    cli -- "MCP bridge: the core's tools for one run,<br/>per-run Unix socket + token (ADR-0036)" --> corep
    mqttdev -.-> bus

    classDef planned stroke-dasharray: 5 5
    class mqttdev planned
```

| Container | Holds | Technology | Phase |
|---|---|---|---|
| Core | Agent loop, router, policies, scheduler, tool registry, Telegram transport (updates by long polling, or by a webhook on the node's public listener, which is off by default and serves nothing else but a health route, ADR-0078); host commands (`host.exec`, `host.stats`) run by the node's own helper process in a Landlock and seccomp sandbox (ADR-0039); the `html2md` module that turns fetched HTML into Markdown | Go; the module in Rust on wazero, embedded in the binary (ADR-0035) | P1 |
| SQLite | Inbox/outbox, conversation history, `turns`, scheduled jobs and `cron_runs`, the tool-call journal `tool_calls`, calls awaiting confirmation and their buttons, `allowed` | modernc.org/sqlite, WAL | P1 |
| Memory repo | `memory/*.md` (facts; held-back ones under `memory/quarantine/`), `persona/*.md`, skills; later embeddings | git, the `git` binary (ADR-0033) | P1 (files), P2 (embeddings) |
| Runner | Module runtime, local policies, offline scheduler | wazero, embedded NATS in P2 | P0 (spike), P1 |
| WASM modules | Compiled reflexes with manifests | TinyGo / Rust / AssemblyScript | P0 (Deye by hand), P3 (by the model) |
| State cells | Desired and reported state of loads, sensors, kill switches, confirmations; backends and observers (ADR-0014) | KV: memory or SQLite in P1; the NATS KV bucket `cells` in P2 (the old tables are renamed and dropped one release later) | P1 |
| API provider client | Chat completions | net/http | P1 |
| claude CLI worker | A conversation provider (ADR-0021); standalone heavy tasks later | unmodified `claude -p` | P1 provider; P2 worker (ADR-0015) |
| MCP bridge | The agent's tools for one `claude -p` run, under the core's policies, taint, journal and call keys | own MCP server in the core on a per-run Unix socket `0600` with a token; the hidden `ganglion mcp-relay` as the CLI's stdio server (ADR-0036, ADR-0037) | P1 (M3b) |
| The web (outside) | Pages and APIs `web.fetch` reads, search results of `web.search`; everything read is data, and it taints the session | HTTP(S) to public addresses only, checked on every connection | P1 (M3b) |
| Bus | Messages between roles; across nodes buffering, KV, object store. In the core it is the embedded NATS server with JetStream on disk in `<data>/bus`, reached in-process; its WebSocket listener, for edges, binds loopback only and is reached through `/bus` on the public listener (R-EDGE-4, ADR-0114), which tunnels bytes and leaves the JWT check to the server, or a server the operator runs (`bus.server: external`) (R-BUS-1, ADR-0106); the embedded server runs in operator mode on the trust chain whose seeds are in the secret store, and an external one with `bus.trust: chain` is pushed the account claims (R-EDGE-1, ADR-0109, ADR-0113) | nats-server embedded, `nats.go` client behind `bus/natsbus` | P2 (M8) |

## Deployment: P1, all-in-one

One process on a Raspberry Pi or a small cloud instance. The bus is
in-process; there is no NATS yet.

```mermaid
flowchart TB
    subgraph host["one host: Pi or cloud instance"]
        subgraph proc["ganglion run --roles core,edge,worker"]
            c["core"]
            e["edge runner"]
            w["worker: API client"]
        end
        db[("SQLite file")]
        logs[("log files<br/>data/logs")]
        repo[("memory git repo")]
        diag["ganglion mcp<br/>read-only, stdio"]
        cl["claude -p, a child per model call<br/>+ ganglion mcp-relay"]
    end
    pc["Owner's PC<br/>claude CLI worker, P2"]
    cc["Claude Code or another MCP client"]
    net["The web"]
    gh["GitHub Releases<br/>module tags, html2md/v*"]
    s3["S3-compatible bucket<br/>daily encrypted backup"]
    c --- e
    c --- w
    c --- db
    c --- repo
    c -- "history on stdin;<br/>tools over MCP on a per-run Unix socket" --- cl
    c -- "web.fetch, web.search" --> net
    c -- "/modules update: sum, ceiling, self-test" --> gh
    c -- "daily at backup.at: the sealed archive,<br/>one signed PutObject over HTTPS (ADR-0095)" --> s3
    c -. "task queue" .- pc
    c --> logs
    diag -- "read-only" --> db
    diag -- "read" --> logs
    cc -- "stdio: local, ssh,<br/>docker exec -i, kubectl exec" --> diag
```

`ganglion mcp` is a separate process started by the MCP client
([ADR-0044](../adr/0044-ganglion-mcp-diagnostics-over-stdio.md)): it
needs no running node and no model, opens the database read-only beside
a running core, and opens no port.

## Deployment: P2, core + edges

The core runs a NATS server with JetStream. Each edge runs a leaf node that
dials out to the core, so no inbound ports or NAT forwarding are needed.
While the link is down, edges keep running on their modules and local
policies; their events stay in the `EVENTS` stream of the edge's own
JetStream domain, and the core's `EVENTS` stream sources it, so they arrive
about a second after the link returns, in order and once
([ADR-0115](../adr/0115-an-edge-keeps-its-events-in-its-own-domain-and-the-core-sources-them.md)).
Every `edge.telemetry_every` the edge sends a snapshot on `g.tel.<edge>`;
the core serves the edges' metrics on its own `/metrics` and shows a line
per edge in `/status`.

```mermaid
flowchart LR
    subgraph server["server or cloud"]
        cn["ganglion run --roles core<br/>+ NATS server, JetStream"]
    end
    subgraph home["home"]
        e1["ganglion run --roles edge<br/>Pi next to the inverter<br/>+ NATS leaf node"]
        esp["ESP32, relays<br/>MQTT"]
    end
    subgraph garage["garage, later"]
        e2["ganglion run --roles edge<br/>+ NATS leaf node"]
    end
    subgraph pc["owner's PC"]
        wk["ganglion run --roles worker<br/>claude CLI"]
    end
    e1 -- "leaf link over wss://, own JetStream domain;<br/>EVENTS sourced by the core; g.tel.edge" --> cn
    e2 -.-> cn
    wk -- "NATS client" --> cn
    esp -- "MQTT" --> e1

    classDef planned stroke-dasharray: 5 5
    class e2 planned
```

An edge needs three files: the binary, a NATS credentials file and the
owner's public key for verifying modules.
