# P1 · M6 task 6.7: updating the node from signed releases — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** from the admin's private chat, the owner moves the node to a newer signed core release
and back; a release without the project's signature is refused; a version that does not start
gives way to the previous one with an alert. Issues
[#193](https://github.com/igorrius/ganglion/issues/193),
[#194](https://github.com/igorrius/ganglion/issues/194),
[#195](https://github.com/igorrius/ganglion/issues/195) (parent
[#188](https://github.com/igorrius/ganglion/issues/188)), shipped in one pull request: none of the
three is safe on a node without the others (owner, 2026-11-15). The daily check
([#196](https://github.com/igorrius/ganglion/issues/196)) is not in this plan.

**Architecture:**

- **`update`** (exists: verification of a release, `release.go`) grows:
  - `version.go`: core versions, `ParseVersion`, `Compare`, the running version's check.
  - `github.go`: the release source over GitHub (the newest stable release, one release by
    tag, the download of its files).
  - `store.go`: `<data>/releases`, its `state.json`, its lock, `Install`, `Rollback`.
  - `launch.go`: the launcher's decision at start (`Plan`) and the fall back.
  - `watch.go`: the node component that confirms a new version and tells the admins.
- **`internal/ghfetch`** (new): the GitHub download client that `modstore/release` has today
  (GitHub's hosts only, public addresses, redirects checked, size limits), moved out so both
  sources use one copy (conventions §8). `modstore/release` keeps its behaviour and its tests.
- **`agent`**: `/update` (`update_cmd.go`) over an `Updater` interface, `NoUpdater` when unset.
- **`cmd`**: `ganglion update [install vX.Y.Z | rollback]`; the launcher step at the start of
  `run`.
- **`config`**: providers of the store, the source, the updater, the watch component; the
  releases folder in the sandbox's denied files and among the node's places.
- **`texts`**: the `/update` texts in English, Russian and Ukrainian.
- No new Go module.

**Spec:** 7.15 (R-UPD-2…5, R-UPD-7), 7.14 (R-CFG-6), 7.12 (R-CMD-3, R-CMD-8);
[ADR-0081](../adr/0081-update-the-node-from-signed-releases-at-the-owners-command.md),
[ADR-0041](../adr/0041-module-install-update-and-trust-boundary.md),
[ADR-0046](../adr/0046-a-restart-is-not-a-failed-attempt.md),
[ADR-0039](../adr/0039-command-sandbox.md). Read them with this plan.

## Global Constraints

- English everywhere; the [conventions](../conventions.md) apply: constructors, no `nil`
  collaborators (a no-op instead), named types for closed sets and function fields, an injected
  clock and `after`, no sleeping in tests, stdlib `testing` only, `config` the only composition
  root, only `cmd/...` imports `urfave/cli`.
- Branch `update-command-193`. Commit after each task with `make lint test` green; stage the
  task's files by path, never `git add -A`. Push after each task.
- Architecture docs change in the same commit as the code: `05-code-map.md` (the `update` row,
  `internal/ghfetch`, the dependency diagram), `03-components.md` if components connect
  differently.
- **Nothing a release page says reaches a reply, the history, a log or the model** (R-UPD-7):
  only the version, the publication date and a link the node builds itself,
  `https://github.com/igorrius/ganglion/releases/tag/<tag>`. The release's `body` and `name`
  are never decoded into a field.
- **The agent gets no tool** and no tool reaches the store (spec 9.8, ADR-0081). `/update` is
  a command, admin only, in the admin's private chat only, with no model.
- **A refusal keeps the running version** and says why in the node's own words (a `texts` key per
  reason), never in an error string from the network.
- Exact values:
  - Folder: `<data>/releases` (`<data>` is the database's folder), mode `0700`; a release is
    `<data>/releases/<version>/` with `ganglion` (`0500`), `SHA256SUMS`, `SHA256SUMS.sig`,
    `release.json` and, when installed by an update, `before.db`.
  - State: `<data>/releases/state.json`, written by a temporary file, `fsync` and rename; lock
    `<data>/releases/.lock` (`flock`, exclusive, non-blocking: a second install is refused as
    busy).
  - Downloads: the binary at most 256 MiB, the small files at most `update.MaxSumsBytes` /
    `MaxManifestBytes` / 1 KiB for the signature; a whole install at most 5 minutes.
  - The image's env file: `/usr/local/share/ganglion/image-env`, given to `config` as a
    bootstrap value (flag `--image-env` on `run` and `update` with that default); missing file:
    no image, the env check is skipped and `/update` says so.
  - Two attempts; a version confirms itself after 2 minutes of its components running.
  - Exit code 75 after an install or a rollback from the chat (`node.ErrRestart`, R-CFG-6).

## Versions (decided)

- A core version is `vMAJOR.MINOR.PATCH` with an optional `-<pre>`, ordered as semver 2.0
  (a pre-release is below its release; pre-release identifiers compared field by field,
  numeric ones as numbers).
- **The running version** is `buildinfo.Read().Version`. Only an exact core version can be
  updated. A build that reports anything else (`dev`, a `git describe` like `v0.2.1-3-gabc`,
  a `-dirty` build) is refused with `update.dev_build`: "this node runs a build without a
  release version; move it to the image `ghcr.io/igorrius/ganglion:<newest>` once, then use
  /update" (owner, 2026-11-15).
- `/update` (status) lists the newest **stable** release; `/update install vX.Y.Z` takes the
  named tag, a pre-release included, when it is newer than the running version.

## State (decided)

`state.json`:

```json
{
  "active": "v0.3.0",
  "previous": "v0.2.1",
  "pending": {"version": "v0.3.0", "attempts": 1, "since": "2026-11-15T12:00:00Z"},
  "fallback": {"from": "v0.3.0", "to": "v0.2.1", "restored_db": true, "reason": "failed", "at": "..."},
  "confirmed": ""
}
```

- `active` "" means the binary of the image or package (the floor); so may `previous`.
- `pending` is set by an install and by a rollback, cleared by the confirmation.
- `fallback` is set by the launcher when it gives up on a version and cleared by the watch
  after it has told the admins.
- `confirmed` is the version the watch last told the admins about, so a restart of a confirmed
  version does not tell them again.
- Unknown fields are refused (`DisallowUnknownFields`): the file is the node's own contract.
- The store keeps the folders of `active` and `previous` and removes every other version's
  folder after a state is written.

## Task 1: `internal/ghfetch`, the shared GitHub client

**Files:** create `internal/ghfetch/ghfetch.go`, `ghfetch_test.go`; modify
`modstore/release/github.go`.

- [x] Move the HTTP plumbing of `modstore/release.GitHub` into `ghfetch.Client`: the host list
  (`api.github.com`, `github.com`, `objects.githubusercontent.com`,
  `release-assets.githubusercontent.com`), `https` on 443 only, `web.PublicDial`, redirects
  checked against the hosts (at most 5), `MaxResponseHeaderBytes`, the test options
  (`WithHosts`, `WithTransport` with plain http). Methods:
  - `Get(ctx, url string, limit int64) ([]byte, error)`: as `get` today.
  - `Copy(ctx, url string, w io.Writer, limit int64) (int64, error)`: streams the body into
    `w`, failing past `limit`.
- [x] `modstore/release.GitHub` holds a `*ghfetch.Client`; its exported options keep working.
  Its existing tests pass unchanged.
- [x] Tests for `Copy`: a body over the limit fails, a redirect to another host fails, a plain
  http URL fails without the test transport.

## Task 2: versions and the release source (#193)

**Files:** create `update/version.go`, `update/github.go` with tests.

- [x] `Version` (parsed), `ParseVersion(string) (Version, error)`, `Compare(a, b) int`,
  `Running(v string) (Version, error)` (`ErrDevBuild` for anything not an exact core version).
  Table tests: ordering of pre-releases, `v1.2.3` vs `v1.10.0`, refusals (`1.2.3`, `v1.2`,
  `v01.2.3`, `v1.2.3-`, describe output, `dev`).
- [x] `Source` over `ghfetch.Client` for repo `igorrius/ganglion`:
  - `Latest(ctx) (Release, error)`: the newest non-draft, non-pre-release whose tag is a core
    version (module tags like `html2md/v0.1.0` are passed over).
  - `Get(ctx, tag) (Release, error)`: that tag's release (`/releases/tags/<tag>`), a draft
    refused.
  - `Release{Version, Tag, Published time.Time, Page string, assets map[string]string}`; `Page`
    is built from the tag, never read from the API.
  - `Fetch(ctx, r Release, dir string, platform string) error`: downloads `SHA256SUMS`,
    `SHA256SUMS.sig`, `release.json` and `BinaryName(platform)` into `dir`. A missing asset is
    `ErrIncomplete` (for the binary: `ErrNoPlatform`).
- [x] Tests against an `httptest` server (as `modstore/release` does): the list with drafts,
  pre-releases and module tags; a redirect to a host not allowed; a release page whose `body`
  holds an instruction does not reach any returned field (abuse test).

## Task 3: the store, install and rollback (#193)

**Files:** create `update/store.go`, `update/store_test.go`.

- [x] `Store` over `<data>/releases` with `Open(dir string, opts ...StoreOption)`: the clock,
  the platform (`runtime.GOOS/GOARCH` by default), the image env (`ImageEnv int`, 0 = no image),
  the keys (`Keys()` by default), the database copier (`CopyDB func(ctx, dst string) error`,
  `VACUUM INTO` on the node's database, wired in `config`).
- [x] `State()`, and `Install(ctx, src Fetcher, version string, running Version) (Installed,
  error)`, in this order; any refusal leaves the state and the active folder untouched:
  1. take the lock (`ErrBusy`);
  2. `ParseVersion`, newer than `running` (`ErrNotNewer`);
  3. `src.Get`, then `Fetch` into `<data>/releases/.incoming-<random>`;
  4. `VerifySums` with the keys (`ErrBadSignature`), `ParseSums`, `Check` of the binary and
     `release.json` (`ErrSumMismatch`), `ParseManifest`, its `version` equals the tag;
  5. `env` above the image's (`ErrEnv`, only when an image env is known);
  6. move the folder to `<data>/releases/<version>` (replacing a stale one), `chmod 0500` the
     binary;
  7. `CopyDB` to `<version>/before.db`;
  8. write the state: `previous` = old `active`, `active` = version, `pending` = {version,
     attempts 0, since now}, `fallback` none; remove the other versions' folders.
- [x] `Rollback(ctx) (to string, error)`: `ErrNoPrevious` when there is none; else `active` and
  `previous` swap, `pending` = the target with attempts 0 (the image binary as a target confirms
  at once: no pending).
- [x] Every refusal is a sentinel error the agent maps to a text: `ErrDevBuild`, `ErrNotNewer`,
  `ErrNotFound`, `ErrNoPlatform`, `ErrIncomplete`, `ErrBadSignature` (and `ErrBadSums`),
  `ErrSumMismatch`, `ErrBadManifest`, `ErrEnv`, `ErrBusy`, `ErrFetch` (network; the cause is
  logged, not shown).
- [x] Abuse tests: a release signed by another key; a binary changed after the sums; a
  `release.json` naming another version; an older version; the same version; a downgrade by a
  pre-release tag; a second install while the lock is held; a state file with an unknown field;
  after each refusal the state file is byte-for-byte what it was.

## Task 4: the launcher (#194, #195)

**Files:** create `update/launch.go`, `update/launch_test.go`; modify `cmd/run.go`; create
`cmd/launch_unix.go` (the `syscall.Exec`).

- [x] `Decide(state, self string, verify func(version) error) Decision` — pure, table-tested:
  - no state, or `active` "": run on (the floor).
  - `self` is the active release's binary (`os.Executable` resolved): run on — this process is
    the release the launcher already started.
  - `pending` with `attempts >= 2`: **fall back**: the version failed; the target is `previous`
    (the floor when ""), `restore` the database when the failed version migrated it.
  - otherwise: verify the active release again (signature with the embedded keys, then the
    binary's sum); a failure falls back as above with the reason `unverified`; success: count
    an attempt when `pending`, then `exec` the active binary with the same argv and environment.
- [x] Fall back: copy `before.db` over the database (removing `-wal` and `-shm`) only when the
  failed version's `schema_migrations` rows differ from `before.db`'s; set `active` =
  `previous`, `previous` = "", `pending` none, `fallback` = {from, to, restored_db, at}; then
  decide again (the target may itself need an `exec`).
- [x] `cmd/run.go`: the launcher runs first in `run`'s action, before the logger and the
  database, from the folder of `--db`. Its own errors (a state it cannot read) are written to
  stderr and the node runs on the floor; it never stops the node from starting.
- [x] Abuse tests: a file planted as `<data>/releases/v9.9.9/ganglion` with a state naming it is
  never executed (the verification fails, the node falls back and the state says so); a binary
  replaced after install is not run; a state naming `../x` as a version is refused.
- [x] `noexec` (#191): when `exec` fails with `EACCES` on the data volume, fall back with the
  reason `noexec` rather than loop.

## Task 5: the watch component, confirm and tell (#195)

**Files:** create `update/watch.go`, `update/watch_test.go`; modify `config` (provider, node
component), `texts`.

- [x] `Watch` is a node component (`Name() "update"`): at start, when `state.fallback` is set,
  it sends one alert to every admin (`alert.Admins`, through the outbox, key
  `update:fallback:<from>:<at>`) — "vX did not start twice; back on vY (database restored |
  database kept)" — and clears it. When the running version is `pending.version`, it waits 2
  minutes on the injected `after` (ctx ending first: nothing), then clears `pending`, sets
  `confirmed`, and sends "now running vX" once (key `update:confirmed:<version>:<since>`, so a later rollback to the same version is told again).
- [x] **A restart is not a failed attempt** (ADR-0046): when a pending version stops with
  `node.ErrRestart` (`/restart`, a config change), `run` takes one attempt back
  (`Store.CleanStop`) before it exits 75, so only starts that end some other way count.
- [x] Tests with a fake clock: confirmation after 2 minutes, none when ctx ends first, the
  fallback alert once, no second "now running" after a restart of a confirmed version.
- [x] Metrics: `ganglion.update.installs` by outcome (installed, refused reason),
  `ganglion.update.fallbacks`, `ganglion.update.confirmations`. Log lines: install, refusal
  (reason, never the network's text), fall back, confirmation.

## Task 6: `/update` and `ganglion update` (#193)

**Files:** create `agent/update_cmd.go`, `agent/update_cmd_test.go`; modify `agent/commands.go`,
`agent/agent.go` (Deps), `texts/texts.go` and the three language files, `cmd/update.go`,
`cmd/root.go`, `config`.

- [x] `agent.Updater` interface: `Status(ctx) (UpdateStatus, error)` (running, newest stable
  release: version, date, page; whether it fits; image env known), `Install(ctx, version)
  (string, error)`, `Rollback(ctx) (string, error)`. `NoUpdater` refuses everything with
  `update.unavailable`. The real one in `update` (`Updater`, composing `Source` and `Store`).
- [x] `/update` for an admin in their private chat only (as `/secrets`), no model:
  - `/update`: running version, newest release with its date and link, and the next step
    (`/update install vX.Y.Z`) when it is newer; "up to date" otherwise; a dev build gets
    `update.dev_build`.
  - `/update install vX.Y.Z`: replies "installing…" first (as `/restart` replies before the
    stop), then installs; on success replies "installed vX; restarting" and calls `Restart`;
    on a refusal replies with the reason's text and stays. Naming the version is the
    confirmation.
  - `/update rollback`: "back to vY; restarting", or "nothing to roll back to".
  - History note: `[command] the owner ran /update: installed v0.3.0` / `rolled back to
    v0.2.1` / `was refused: <reason key>`. Never any text from the release.
  - Command hints in English, Russian and Ukrainian in the command table.
- [x] Tests: a non-admin and a group are refused; a refusal leaves no restart; success calls
  `Restart` once; the release's notes never appear in a reply or note (abuse test with a fake
  source whose release body holds an instruction).
- [x] `ganglion update [--db] [--image-env]`: the status; `ganglion update install vX.Y.Z`:
  installs, then prints "installed; restart the node to start it" (the host command does not
  stop a running node); `ganglion update rollback` likewise. Exit 1 on a refusal with the same
  words in English.

> Done as planned, with two choices: "installing…" goes through the outbox with a key of its own
> (`Deps.Outbox`, as a button's acknowledgement does), ahead of the turn's reply; and a node that
> starts with an image env file it cannot read treats it as no image instead of failing to start.

## Task 7: the sandbox, the places, the docs (#193, #194)

**Files:** modify `sandbox/deny.go` (`NodeFiles.Releases`), `config/sandbox.go`,
`config/host.go` (`host.PlaceReleases`, no age), `host/places.go`; docs.

- [x] `<data>/releases` is denied to host commands (read and write); a test in `sandbox` that a
  command cannot list or write it.
- [x] `host.stats` shows the releases folder.
- [x] Docs in the same commit:
  - `docs/guide/running/operations.md`, "Updating": replace "There is no built-in updater yet"
    with `/update`, `/update install`, `/update rollback`, `ganglion update`, the two starts and
    the fall back, the dev-build note, and the image path for what `/update` does not change
    (Rust, the claude CLI: `env`).
  - `docs/releasing.md`: how a node takes a release.
  - `docs/deploy-railway.md`: the one-time switch of the service to the image, then `/update`.
  - `05-code-map.md`, `03-components.md` (the watch component, the launcher before the node).
  - Spec 7.15: nothing changes unless the code differs from it; then the spec first.
  - `docs/README.md` "Where we are"; `docs/plans/p1-all-in-one.md` task 6.7 progress.

## Task 8: lab check and live check

- [ ] An automated lab test of the whole path (two built versions, a test key) — not built; the
  manual check below covers it once. Follow-up if the path changes.
- [x] Manual end-to-end check, 2026-11-15, on a developer machine: a copy of the repository with
  a throwaway test key added to `update/keys`, the node built as `v0.2.1` (the floor) and as
  `v0.3.0`, release folders signed with that key by hand (`openssl pkeyutl`):
  - the floor verified `v0.3.0`, counted attempt 1 and `exec`ed it; the node logged
    `version=v0.3.0`;
  - after two minutes of running, `update: confirmed`; `state.json` had no pending and
    `confirmed: v0.3.0`;
  - a release `v0.4.0` whose binary exits at once: two starts died, the third start logged
    "v0.4.0 is given up (failed); back on v0.3.0", the node ran `v0.3.0`, the watch logged the
    fall back and cleared it;
  - the active binary replaced by a script after install: it did not run; "v0.3.0 is given up
    (unverified); back on the binary of the image", the node ran `v0.2.1`.
  - Not covered: the database restore on real SQLite files (unit-tested with a fake; the
    migrations listing is tested on a real database), the admins' messages (the test node had
    no admins), a real download from GitHub.
- [x] Live, 2026-11-15, on the owner's Railway node:
  - the service was moved once from `railway up` builds (version `dev`) to the image
    `ghcr.io/igorrius/ganglion:0.3.0`; the node started as `v0.3.0` with its modules and webhook;
    `/update` said it was up to date;
  - `/update install v0.3.1`: "installed, restarting"; the node started as `v0.3.1` three
    seconds later and confirmed itself two minutes after;
  - the silence between the restart and the confirmation was not friendly (owner): v0.3.2
    (#202) tells the admins at once that a version started, and after a rollback which version
    runs;
  - `/update install v0.3.2`: "installed, restarting", "v0.3.2 has started…" at once, the
    confirmation two minutes later;
  - `/update rollback`: back on `v0.3.1` (older code, so no "started" message) and its
    confirmation two minutes later; `state.json` showed `active v0.3.1`, `previous v0.3.2`,
    `confirmed v0.3.1`.
  - Not seen live: a fall back after two failed starts, a database restore.

## Done when

The issues' "done when" for #193, #194 and #195 hold; `make lint test` is green; the pull request
says `Fixes #193`, `Fixes #194`, `Fixes #195`.
