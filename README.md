# ganglion

A distributed agent network in Go. The agent designs automations with a
model, then compiles regular tasks into small WASM modules ("reflexes") that
run on edge nodes without the model. The model is called only on exceptions.

Status: early R&D, nothing runs yet. Versions are 0.x and the API is unstable.

## Shape

- **core** — conversation history, memory, the model router, the tool
  registry, guaranteed delivery (inbox/outbox).
- **edge** — the runner next to devices: WASM modules (wazero), local
  policies, a scheduler, an embedded NATS leaf node for offline buffering.
- **worker** — model execution where resources are (API providers, Ollama,
  the user's own Claude Code CLI).

One binary; roles are enabled with flags. A single Raspberry Pi can carry
all three.

## First milestone

The Deye battery monitor as a WASM module, plus staged load shedding through
Tuya smart plugs: below 50% charge the big boiler turns off, below 40% the
kitchen one. It runs without a model and escalates only on errors.

## Building

Requires Go 1.27 and, for linting, golangci-lint v2 built with Go 1.27.

```sh
make check                      # lint, test, build
./bin/ganglion version          # version, commit, toolchain

# Run: settings live in ganglion.yaml beside the database; secrets stay in the environment.
cp ganglion.example.yaml ganglion.yaml   # set telegram.admin and the models you use
export TELEGRAM_BOT_TOKEN=...                      # and the provider keys the file names
./bin/ganglion run --roles core,edge,worker

make example-deye modrun        # build the Deye example module (Rust) and the module runner
./bin/modrun -wasm bin/deye-reader.rust.wasm \
  -manifest examples/modules/deye-reader/manifest.yaml -config inverter_sn=<serial>
```

The Deye reader is an example the agent copies into its workspace, builds
and installs ([examples/modules/deye-reader](examples/modules/deye-reader/README.md)).
`make modules` and `make modules-tinygo` build the Go reference reader
(`internal/deyeref`) with Go and TinyGo 0.44+; `make bench` compares the
builds. Module secrets are read from `.env`.

## Design

The implementation spec — requirements, architecture, protocol drafts and
the phased plan with exit gates — is in [docs/spec.md](docs/spec.md).

The underlying product and architecture decision lives in a design document
(private for now).

## License

Apache License 2.0, see [LICENSE](LICENSE).
