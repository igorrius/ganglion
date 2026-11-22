# reader-template

The starting point for a reader: a module that reads an outside service on a
schedule and reports what it read as cells (spec R-RDR-1..4). The agent does
not write a module from nothing; it imports this one under a new name
(`module.import`), changes two files, builds (`module.build`) and offers the
result to the owner (`module.install`). The `module-writing` skill describes
the loop and says when a reader is the right kind of module.

## What the agent changes

Only these two files:

- `manifest.yaml`: the name, the schedule, the hosts it may call
  (`http.fetch`), the secrets it may read (`secret.get`), the settings
  (`config`), the cells it reports and the `sample` type of the status tool.
- `src/logic.rs`: what is read and how, as the five functions and two
  constants of `Source`, with a unit test for each:
  - `SECRETS`: the same names as the manifest's `secret.get`;
  - `STALE_AFTER_SECS`: how old a reading may be and still be reported;
  - `settings`: the config, checked without network;
  - `request`: the one request of a read;
  - `parse`: the answer as a `Reading`, or which field is missing;
  - `taken_at`: when the service took the reading;
  - `cells`: the cells besides `sample`.

Everything else stays as it is: `src/engine.rs` keeps the rules below and
names no service and no setting, so its tests never break when the service
changes; `src/host.rs` is the host as a trait, `src/guest.rs` the ABI glue
for `wasm32` ([ADR-0011](../../../docs/adr/0011-module-abi.md)),
`src/fake.rs` the host the tests use. `Cargo.lock` is committed and lists
the only crates a reader may use: `serde`, `serde_json` and `base64`. The
node builds with `--locked`.

## The rules the engine keeps

| Trigger | What happens |
|---|---|
| cron | Reads the service and reports the cells; no model is involved. |
| tool `status` (the agent sees `<module>.status`) | Reads fresh under the same rules; when the service cannot answer, it returns the last reading from the `sample` cell with `fresh: false` and its age. With no reading yet it answers the read's own failure, never an answer outside its declared output. |
| `health` (at install and on the module's card) | Checks without network that the settings parse and every secret is set. It cannot tell a wrong secret from a right one: the first read does. |
| `init`, anything else | "unknown trigger": loading the module makes no request. |

- **HTTP 429.** It stores `not_before` from `Retry-After` (seconds or an
  HTTP date; 60 s when absent, at most a day) and makes no request until
  then: cron answers OK, the tool answers the cell.
- **Stale data.** A reading older than `STALE_AFTER_SECS` reports nothing.
  The first run of such an episode answers `stale` (the node escalates it to
  the agent); later runs answer OK until a fresh reading ends the episode.
- **Refused secrets.** HTTP 401 or 403 answers `auth_failed`, and the same
  secrets are never tried again, so a wrong password cannot get an account
  locked; changing a secret allows one new attempt. The state keeps a 16-bit
  mark of the secrets to notice the change, never the secrets.
- **One request per read.** A service that needs a login before the read
  (a token to fetch and keep) does not fit `request` as it is: say so to the
  owner before changing `engine.rs`, and keep its rules when you do.

## The example service

`GET https://api.example.com/v1/stations/<station>/latest` with a bearer
token answers `{"ts": 1793000000, "value": 21.5, "online": true}`. The
template reports the cells `value`, `online` and `sample` (the node names
them `<module>.<cell>`,
[ADR-0062](../../../docs/adr/0062-cells-owned-by-modules.md)). A reflex made
from `reflex-template` can listen to them: that is how modules are linked.

- Config: `station`, in the module's line of `ganglion.yaml`.
- Secret: `API_TOKEN` (`ganglion secret set --module <name> API_TOKEN`).
- Network: `api.example.com` only.

## Events (optional)

The reader publishes nothing by default. To let other modules be linked to it, declare outputs
in `manifest.yaml` (`ports: emits: {reading: {value: number, online: boolean}}`) and call
`h.emit("reading", &json!({"value": v, "online": true}))` in `engine.rs` after the cells were
reported. `emit` returns `Err(Emit::Denied)` for a port the manifest does not declare,
`Err(Emit::Format)` when the payload does not fit the port's shape (the run then fails as
`module.format` whatever the module answers), `Err(Emit::NotHere)` in `init` and `health`, and
`Err(Emit::Failed)` for a payload over 64 KiB, more than 100 events in a run, or a node that could
not publish. Nothing is sent on an error. The `Fake` host records what was emitted
(`emitted`) and can refuse (`emit_verdict`). Delivery is at least once.

## Developing

```sh
cargo test --locked      # the engine's rules and the service's functions on a fake host
cargo clippy --locked --all-targets -- -D warnings
cargo fmt --check
cargo clippy --locked --target wasm32-wasip1 -- -D warnings
make example-reader      # bin/reader-template.wasm, built as the node builds it
go test ./tools/modbench -run TestTheReaderTemplate   # with GANGLION_NET_TESTS=1: built by the node's builder, run on recorded answers
./bin/modrun -wasm bin/reader-template.wasm \
  -manifest examples/modules/reader-template/manifest.yaml -config station=roof-1
```
