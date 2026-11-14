# ganglion

A self-hosted agent in Go that talks to you in Telegram, remembers, and
builds its own automations. A routine task the agent has worked out is
compiled into a small WASM module (a "reflex") that then runs on a schedule
with no model at all; the model is called for conversation and for
exceptions.

The long-term shape is a network of nodes: a core that holds conversations
and memory, edge runners next to devices, and workers where the model
runtimes are. Today it is one process that carries all three roles.

## Status

**Early, 0.x, no guarantees.** One person builds it on weekends, with
Claude writing most of the code. The repository is open so that people can
read it, try it and report bugs; it is not a released product. Formats,
commands and the configuration file change without notice, and an issue may
wait for an answer. The public release is planned for a later phase
([ADR-0082](docs/adr/0082-open-the-repository-now-and-keep-the-announced-release-in-p3.md)).

One node has been running the author's family bot since November 2026.

## What works today

- **A Telegram agent** in private chats and in groups (it answers in a
  group only when addressed), with an access list: strangers get no reply.
- **Models by purpose** from one file: the `claude` CLI on your own
  subscription, or any OpenAI-compatible API.
- **Memory** kept by topic in plain files, sessions, reminders and
  scheduled tasks.
- **Tools under policies:** web search and reading, host commands in a
  sandbox (Landlock, no new privileges, a clean environment), each
  state-changing action confirmed by a button.
- **Reflex modules:** the agent writes a module in Rust, the node builds it
  to `wasm32-wasip1` and, after the owner approves, runs it in
  [wazero](https://wazero.io) on a schedule, with only the capabilities
  its manifest asks for. Modules are versioned and roll back.
- **Skills:** procedures in Markdown the agent reads before it acts.
- **An encrypted secret store**, one configuration file with a JSON schema,
  logs and a tool-call journal, and read-only diagnostics over MCP
  (`ganglion mcp`).

Not built yet: nodes talking to each other (the network of the name),
prebuilt binaries and images, updating a node from a release, a user guide.
[docs/README.md](docs/README.md) says where the work is right now.

## Build and run

Needs Linux and Go 1.27. Host commands and module builds need kernel 6.2 or
later (Landlock); building modules needs Rust with the `wasm32-wasip1`
target. Linting needs golangci-lint v2 built with Go 1.27.

```sh
make build                      # ./bin/ganglion
./bin/ganglion version

cp ganglion.example.yaml ganglion.yaml
# In ganglion.yaml: set telegram.admin to telegram:user:<your Telegram id>
# and choose the providers and models you have.

export TELEGRAM_BOT_TOKEN=...   # from @BotFather; plus the provider keys the file names
./bin/ganglion run
```

The file holds every setting and no secret: a secret is named there as
`env:NAME` (an environment variable) or `store:NAME` (the node's encrypted
store, filled with `ganglion secret set NAME`). The node keeps its database
beside the file.

In a container: the [`Dockerfile`](Dockerfile) builds an image with the
node, Rust and the `claude` CLI, running as an unprivileged user with its
data on a volume at `/data`.
[docs/deploy-railway.md](docs/deploy-railway.md) is the runbook for one
hosting platform; most of it applies to any.

The agent starts a module from one of two templates in
[examples/modules](examples/modules): a reader that brings an outside
service's numbers into cells, and a reflex that reacts to cells and writes
to the owner.

## Documentation

`docs/` is the project's engineering record, written as the work goes. It
is not a user guide yet.

| Question | Read |
|---|---|
| Where is the project now? | [docs/README.md](docs/README.md) |
| What is being built, and in what order? | [docs/spec.md](docs/spec.md) |
| What does it look like inside? | [docs/architecture/](docs/architecture/README.md) |
| Why was it decided this way? | [docs/adr/](docs/adr/README.md) |
| How is the code written? | [docs/conventions.md](docs/conventions.md) |

## Reporting bugs and vulnerabilities

Bugs go to [GitHub Issues](https://github.com/igorrius/ganglion/issues).
A vulnerability is reported privately: see [SECURITY.md](SECURITY.md).
Before a pull request, read [CONTRIBUTING.md](CONTRIBUTING.md).

## License

Apache License 2.0, see [LICENSE](LICENSE).
