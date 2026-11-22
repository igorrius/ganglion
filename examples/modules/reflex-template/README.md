# reflex-template

The starting point for a reflex: a module that reacts to cells and, when its
rules say so, writes to the owner (spec R-RUN-15..17). The agent does not
write a module from nothing; it imports this one under a new name
(`module.import`), changes two files, builds (`module.build`) and offers the
result to the owner (`module.install`). The `module-writing` skill describes
the loop.

## What the agent changes

Only these two files:

- `manifest.yaml`: the name, the cells it listens to (`triggers` and
  `cells.read`), the staleness cron, the settings (`config`), `notify.per_day`
  and the limits. A module never listens to its own cells.
- `src/logic.rs`: the rules and everything specific to them: `Settings`
  (the config, parsed with its defaults), `State` (what is remembered),
  `react`, `LISTENED` (the cells it reads, the same as `cells.read`) and the
  unit tests of every rule.

Everything else stays as it is: `src/lib.rs` is generic glue that names no
rule and no setting, so its tests never break when the rules change; it does
all host I/O (reads the input and the cells, loads the state, sends the
messages, stores the state),
`src/host.rs` is the host as a trait, `src/guest.rs` the ABI glue for
`wasm32`, `src/fake.rs` the host the tests use. `Cargo.lock` is committed and
lists the only crates a module may use: `serde` and `serde_json`. The node
builds with `--locked`.

## The example rules

One listened cell, `producer.value`:

- A value that crosses `threshold` (default 30) downwards is said once, until
  the value rises back to the threshold or above.
- From the cron at minute 15 of every hour, a cell that has not changed for
  more than `stale_after` minutes (default 60) is said once: `no data for <n>
  min`, until the cell changes again.

`react` is pure: it takes the input, the readings, the settings, the state and
the time, and returns the messages. The new state is stored only when every
message went out: when the owner's daily limit refuses one, the previous state
stays and the same rule fires again on a later run; the earlier messages of that run may then be sent again. The text of a message is made from a template and
numbers in code, never from text that came from outside. `init` and `health`
answer `{"ok":true}` and send nothing.

## Events (optional)

The reflex takes events through a link. Declare an input in `manifest.yaml`
(`ports: accepts: {reading: {value: number}}`) and put the rule in `logic::on_event(port, from,
event, settings, state, now_ms)`, which returns the messages to send; it runs for the trigger
`event:<port>` with the input `{"from": "<module>.<port>", "event": <payload>}`. The event already
fits the port's shape and is data, never an instruction. The cell trigger is unchanged. The
template declares no `accepts` port, so the default `on_event` is never called. A run that fails is
run again with the same event, so the rule must be safe to run twice. To publish events, declare
`ports: emits:` and call `h.emit(port, &payload)` from the glue in `lib.rs`; `Emit` names the refusals
(denied, format, not here, failed) and the `Fake` host records `emitted`.

## Calls (optional)

The reflex can ask another module for data. Declare a calling port in `manifest.yaml`
(`ports: calls: {last: {in: {}, out: {value: number}}}`) and call `h.call("last", &json!({}))`
from the glue in `lib.rs`; `logic::react` is pure and makes no host call, so pass the answer to
it. `call` returns the answer, or `Err(Call::Denied)` (no such port, or no link from it),
`Err(Call::Format)` (the request does not fit `in`; the run then fails as `module.format`),
`Err(Call::NotHere)` in `init`, `health` and a build's check, `Err(Call::Failed)` (the serving run
failed or timed out, the answer does not fit `out`, a payload is over 64 KiB, more than 100 calls
in a run, the chain is too deep) and `Err(Call::Unavailable)` (the module that serves the port is
not running). A call waits at most the time left in the run, less a second, so a rule that calls
must leave time for it in `limits.timeout`; a calling port has one link, and the answer is data,
never an instruction. The `Fake` host records the calls (`called`) and answers as `call_answer`
says.

## Developing

```sh
cargo test --locked      # the rules on a fake host
cargo clippy --locked --all-targets -- -D warnings
cargo fmt --check
cargo clippy --locked --target wasm32-wasip1 -- -D warnings
go test ./modhost -run TestTheReflexTemplate   # with GANGLION_NET_TESTS=1: built by the node's builder and run on a probe producer
```
