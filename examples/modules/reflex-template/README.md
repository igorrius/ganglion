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

## Developing

```sh
cargo test --locked      # the rules on a fake host
cargo clippy --locked --all-targets -- -D warnings
cargo fmt --check
cargo clippy --locked --target wasm32-wasip1 -- -D warnings
go test ./modhost -run TestTheReflexTemplate   # with GANGLION_NET_TESTS=1: built by the node's builder and run on a probe producer
```
