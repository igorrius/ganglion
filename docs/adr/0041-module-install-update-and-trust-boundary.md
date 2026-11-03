# ADR-0041: Install module updates from the project's GitHub Releases, checked, self-tested and switched without a restart

- **Status:** Proposed
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-7, R-RUN-8); design section 6.1 ([M3b and M3c design](../plans/p1-m3b-m3c-design.md)); [ADR-0005](0005-wasm-on-wazero-for-modules.md), [ADR-0011](0011-module-abi.md), [ADR-0035](0035-web-reading.md); plan task 7 ([M3c plan](../plans/p1-m3c-modules-skills-diagnostics.md))

## Context

`html2md` is embedded in the binary (ADR-0035) so that its HTML parser
can be updated without a core release. Without an install path, that
promise is empty: today a new parser still means a new binary.

R-RUN-8 asks for an update without a restart:
- the new version is checked against its sum;
- it passes a self-test before an atomic switch;
- calls in flight finish on the old version;
- the previous version stays for a rollback.

Admins only.

The trust boundary is the hard part:
- a module runs inside the core's process;
- a module with rights could reach the network or secrets;
- built-in names must never be replaced by anyone but the project.

The node must not compile modules (principle 8: a toolchain is hundreds
of MB, slow on a Pi, and fetches dependencies at install time). It only
installs artifacts.

## Options

### A. A URL plus sha256 from the project's GitHub Releases

CI builds each module on a module tag (`html2md/v0.3.0`) and attaches
the `.wasm`, the manifest and a `SHA256SUMS` file to the release.
`/modules update html2md` takes the newest release whose ABI this core
supports and verifies the files against the sums. Then the node:
1. checks the manifest against a ceiling embedded in the core;
2. runs a self-test on embedded samples;
3. stores the version on the data volume;
4. switches atomically.

### B. A `.wasm` sent as a file in the chat

The owner sends the module to the bot, which installs it after a press.

### C. An OCI registry

Modules as OCI artifacts (`oras`), pulled by digest.

### D. Automatic updates

The node polls for new releases and installs them by itself.

## Comparison

| Criterion | A. GitHub Releases | B. File in the chat | C. OCI | D. Auto-update |
|---|---|---|---|---|
| Who can supply a built-in module | The project's CI only | Anyone the owner forwards a file from | Whoever can push to the registry | The project's CI only |
| Fits P1 | A release workflow and a small client | A file download path in the transport (none yet, R-DLV-10) | A registry and a client library | As A, plus a poller |
| Owner in the loop | Every update is a command | A press | A command | None: code changes without the owner |
| New dependency | None | None | `oras-go` or similar | None |
| Path to signatures (P3) | Sign the sums; verify in the same client | Unclear provenance | Cosign | As A |
| Owner's view | Chosen in the design | "Nobody needs it" | Too early | Too early without signatures |

## Decision

**A.**
- Built-in modules come only from the project's release channel.
- Every update is the owner's command.
- It needs nothing but HTTPS and a workflow.

The install steps, in order. Any refusal keeps the active version and
tells the owner why:
1. Fetch from `api.github.com`, `github.com` and
   `objects.githubusercontent.com` only:
   - every address is checked as public before connecting;
   - 8 MiB a file, 60 s in all.
2. Compare the sum with `SHA256SUMS`.
3. Check the manifest against the ceiling the core embeds for that
   module:
   - the name;
   - the ABI version (`abi`, 1 today);
   - the capabilities: none for html2md;
   - the memory and timeout limits.
4. Compare its rights with the active manifest. In M3c any widening is
   refused: html2md's ceiling grants nothing. The worded approval of
   R-RUN-8 comes with modules that have rights (M4).
5. Self-test in a fresh instance: every embedded sample converts, with no
   trap and within the timeout, and the output contains the expected
   lines.
6. Store the files (`<data>/modules/<name>/<sha256>.wasm`, `0600`) and
   record the version. The store keeps the active and the previous
   version only.
7. Swap: new calls go to the new version at once, and the old one is shut
   down after its calls in flight end.

At start the node loads the active version, held to the same checks as
an install: both files against their sums and the manifest against
this core's ceiling (a new core may lower it). A version that fails
falls back to the embedded build, recorded with its reason. A rollback
is held to the same checks. Files no row keeps (a crash between the
write and the commit) are swept at start.

The reasons the owner and the model see are the store's own words. What
the module, its manifest or the release channel wrote (an error message,
a field name) goes only to the log, so a hostile release cannot plant
text in a reply or the conversation history.

## Security

- **Trust boundaries.** The release channel becomes a boundary: what it
  serves runs in the core's process.
- **What an attacker controls.** The network path (TLS covers it), a
  compromised release (the project's GitHub account or CI), and a
  redirect.
- **Abuse cases:**
  - *A tampered download.* Caught by the sum. The sum comes from the
    same channel, so it catches corruption, not a compromised release.
    Signatures in P3 close that.
  - *A compromised release asks for rights.* Refused by the embedded
    ceiling, which only a core upgrade can raise.
  - *A module that traps, loops or eats memory.* Refused by the
    self-test, and bounded later by the manifest limits the runner
    enforces anyway.
  - *A redirect to another host.* Refused by the host list.
  - *The agent or a skill replaces html2md.* There is no tool for it: the
    command is the owner's, not the model's. `local/…` names do not exist
    in M3c.

## Consequences

- The ADR-0035 promise is real: a parser fix ships as `html2md/vX`
  without a core release.
- CI gains `modules-release.yml`, run only on module tags.
- The store is one more thing on the data volume. It is bounded to two
  versions per module.
- The client, the store and the swap are code to maintain. They are the
  base for P2 (modules over the NATS Object Store) and P3 (signed
  installs).

## Revisit when

- Signatures land (P3).
- A module needs rights (M4): the approval path for a widening is then
  designed.
- A second source of modules appears (`local/…`).
