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

Requires Go 1.26 and, for linting, golangci-lint v2.

```sh
make check                      # lint, test, build
./bin/ganglion --version
./bin/ganglion --roles core,edge,worker

make modules modrun             # build the Deye module (Go) and the module runner
./bin/modrun -wasm bin/deye-reader.go.wasm \
  -manifest modules/deye/deye-reader.yaml -config inverter_sn=<serial>
```

`make modules-tinygo` and `make modules-rust` build the same module with
TinyGo 0.44+ and Rust (`wasm32-wasip1`); `make bench` compares them. Module
secrets are read from `.env`.

## Design

The implementation spec — requirements, architecture, protocol drafts and
the phased plan with exit gates — is in [docs/spec.md](docs/spec.md).

The underlying product and architecture decision lives in a design document
(private for now).

## License

Apache License 2.0, see [LICENSE](LICENSE).
