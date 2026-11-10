# deye-reader

The first module of the owner's system (spec section 8, R-DEYE-1..3): it
reads a Deye hybrid inverter through the DeyeCloud OpenAPI and reports three
sensor cells. It is an example, not a part of the agent: the agent copies it
into its workspace (`module.import`), builds it from source (`module.build`)
and installs it after the owner approves its rights (`module.install`,
[ADR-0061](../../../docs/adr/0061-local-modules-built-from-source.md)).

## What it does

| Trigger | What happens |
|---|---|
| cron, every 30 minutes | Reads the inverter and reports the cells; no model is involved. |
| tool `status` (the agent sees `deye-reader.status`) | Reads fresh under the same rules; when the cloud cannot answer, it returns the cells' last values with `fresh: false` and their age. With no reading in the cells (never reported, so null) it answers the read's own failure (`auth_failed`, `stale` or `module.exception`), never an answer outside its declared output. |
| `health` (at install and on the module's card) | Checks without network that `inverter_sn` is 8 to 16 digits and that the four `DEYE_*` secrets are set; otherwise `module.exception`. It cannot tell a wrong password or serial number from a right one: the first read does. |
| `init`, anything else | "unknown trigger": loading the module makes no request. |

The cells (the node names them `deye-reader.<cell>`,
[ADR-0062](../../../docs/adr/0062-cells-owned-by-modules.md)):

| Cell | Type | Value |
|---|---|---|
| `soc` | integer | battery state of charge, % |
| `grid` | boolean | grid present (L1-L2 voltage above 170 V) |
| `sample` | object | the whole reading, the 12 fields of `deye.py`: `ts`, `soc`, `batt_v`, `batt_w` (negative is charging), `batt_a`, `grid_v`, `freq`, `grid_w`, `inv_w`, `temp_dc`, `temp_hs`, `grid_on` |

The rules it keeps:

- **HTTP 429.** It stores `not_before` from `Retry-After` (seconds or an
  HTTP date; 60 s when absent, at most a day) and makes no request until
  then: cron answers OK, the tool answers the cells.
- **Stale data.** A reading older than 20 minutes reports nothing. The first
  run of such an episode answers `stale` (the node escalates it to the
  agent); later runs answer OK until a fresh reading ends the episode.
- **A refused login** answers `auth_failed` and is never tried again with
  the same e-mail and password, so a wrong password cannot get the account
  locked; changing either secret allows one new attempt.

## Settings

- Config: `inverter_sn`, the inverter's serial number, in the module's line
  of `ganglion.yaml`.
- Secrets: `DEYE_APP_ID`, `DEYE_APP_SECRET`, `DEYE_EMAIL`, `DEYE_PASSWORD`.
- Network: `eu1-developer.deyecloud.com` only.

## Files

- `manifest.yaml`: triggers, the status tool's output, cells, rights,
  config, limits and escalations.
- `src/logic.rs`: the triggers and the rules above, with their unit tests.
- `src/cloud.rs`: the DeyeCloud read (login, token cache, `/device/latest`).
- `src/host.rs`: the host functions as a trait; `src/guest.rs`: the ABI glue
  for `wasm32` ([ADR-0011](../../../docs/adr/0011-module-abi.md)).
- `Cargo.lock` is committed: the node builds with `--locked`.

## Developing

```sh
cargo test                       # the unit tests (CI runs them with clippy and fmt)
make example-deye                # bin/deye-reader.rust.wasm, built as the node builds it
go test ./tools/modbench -run TestTheRustExample   # built by the node's builder, compared with internal/deyeref
./bin/modrun -wasm bin/deye-reader.rust.wasm \
  -manifest examples/modules/deye-reader/manifest.yaml -config inverter_sn=<serial>
```

The Go reader it was ported from, `internal/deyeref`, stays as the
reference: the Go test above runs both on the recorded DeyeCloud answers in
`tools/modbench/testdata` and requires the `sample` cell to equal the Go
reader's sample.
