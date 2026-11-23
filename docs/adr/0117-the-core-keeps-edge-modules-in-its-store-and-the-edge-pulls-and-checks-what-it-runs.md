# ADR-0117: The core keeps edge modules in its own store and delivers the active version; the edge pulls it across the core's JetStream domain and checks the digest and the health before it runs it

- **Status:** Accepted
- **Date:** 2026-11-23
- **Deciders:** Igor Artasevych
- **Spec:** R-EDGE-2, R-EDGE-6, [9.2](../spec.md#92-module-updates-desired-state-p2), [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules); builds on [ADR-0061](0061-local-modules-built-from-source.md), [ADR-0109](0109-a-trust-chain-in-the-vault-and-edges-joined-by-a-one-time-code.md), [ADR-0115](0115-an-edge-keeps-its-events-in-its-own-domain-and-the-core-sources-them.md); [design](../design/p2-bus-and-edge-design.md) section 7.7

## Context

R-EDGE-6 says a module with `node: <edge>` in `modules:` is delivered by
desired state: the `.wasm` and manifest in the `modules` Object Store,
`desired.<edge>.<module>` with version and SHA-256, `actual.<edge>.<module>`
written back after `init` and `health`; a failed health keeps the old
version; rollback writes the previous version to `desired`. The P2 gate
asks that a module on an edge updates and rolls back with one command.

Building it leaves these questions:

1. Where the core keeps the versions of an edge's module, so update and
   rollback stay the commands they are (`/modules rollback`, the install
   window).
2. How the edge reaches the bucket and the files. The edge runs its own
   JetStream domain (ADR-0115); the core's server had no domain, so an edge
   could not address the core's JetStream at all.
3. What the edge checks before it runs anything, and what it does when a
   check fails.
4. Where the health of an edge's module is checked: the core checks a
   build's health before an install (R-RUN-9), but a module meant for an
   edge reads what only the edge reaches (a device on its network).
5. What the edge's user must be allowed, for the join (#322) to issue it.

## Options

### Where the versions live

- **A1.** The core's module store: an edge module is installed like any
  other (journal, active and previous version, files on disk); the core
  does not run it and delivers its active version. Rollback is the store's
  rollback, which delivers the previous version.
- **A2.** The bucket's history: the core keeps no files of edge modules;
  rollback reads the previous value of `desired.<edge>.<module>`.

### How the edge reads

- **B1.** Pull: the core's server takes the JetStream domain `core`; the
  edge watches its desired keys and reads the files through
  `$JS.core.API`, and writes its actual keys there.
- **B2.** Push: the core writes into the edge's own domain
  (`$JS.<edge>.API`), which it already reaches for the edge's events.

### Where the health runs

- **C1.** On the edge only: the core loads the build (it must compile) and
  skips its health trigger when the line names an edge.
- **C2.** On both.

## Comparison

| Criterion | A1 | A2 |
|---|---|---|
| Update and rollback commands | unchanged | new code for edges |
| Journal, card, version rule (a version above every one before) | as for any module | none |
| What survives a lost bucket | everything: the next start delivers again | nothing |

| Criterion | B1 | B2 |
|---|---|---|
| Matches R-EDGE-2 and ADR-0109 (the edge subscribes to `desired.<edge>.>`) | yes | no: needs a superseding ADR |
| An edge offline at the change | reads it when its watch starts again | the core must retry per edge |
| Change to the core | `JetStreamDomain = core` on the embedded server | none |
| What the edge's user needs | read its keys and the objects, write its actual keys | nothing new |

| Criterion | C1 | C2 |
|---|---|---|
| A module reading a device on the edge's network | installs | refused on the core, where the device is not |
| A build that does not even load | refused on the core | refused on the core |

## Decision

A1, B1, C1.

- **Settings:** `node: <edge>` in a module's line (one token, not `core`;
  `core` and none run it on the core). The core's host does not run such a
  module; after every start and reload it writes the active version's
  files, then `desired.<edge>.<module>`, and deletes the module's desired
  key on every other edge. A removed or turned-off module is withdrawn
  from every edge, and its files go.
- **Names:** KV bucket `delivery` in the core's domain, history 8:
  `desired.<edge>.<module>` and `actual.<edge>.<module>`, the module by its
  short name. Object Store `modules`: `<module>.<digest>.wasm` and
  `<module>.<digest>.yaml`, written once; the core prunes the module's
  other builds except the one the edge reports running.
- **Values:** desired `{v, module, version, sha256, config}`, where
  `sha256` is the build's digest (modstore.Digest over the `.wasm` and the
  manifest, ADR-0061) and `config` the line's keys that the manifest
  declares. Actual `{v, module, version, sha256, desired, result, reason,
  at}`, `result` one of `running`, `refused`, `removed`.
- **Domain:** the core's embedded server runs with
  `JetStreamDomain = core` (bus.CoreNode). An external server serving
  edges needs the same domain.
- **The edge (a converger):** it watches `desired.<edge>.*` while its link
  is up, starts the watch again when the link returns and every 5 minutes,
  and applies each key in order: the key's edge must be its own; the
  value must be well formed and name the key's module; if that digest
  runs, it only writes the config; otherwise it fetches both files (16 MiB
  each at most), refuses them unless their digest is the one in the key,
  checks the build in a throwaway instance with its health trigger, writes
  the module's line with its config, installs it (`InstallDelivered`: no
  version or link check, which are the core's) and reloads it, which runs
  `init`. If the reload fails it rolls back to the version before, or
  removes a first version. Then it writes `actual`. A refused digest is not
  fetched again until the core names another or the edge restarts. A
  deleted desired key takes the module out of service (its secrets and
  cells stay).
- **Rollback** on the core is `/modules rollback`: the store's previous
  version becomes active and is delivered; the edge holds it as its
  previous version and switches back.
- **The edge's user** (`natsbus.DeliveryPermissions`, for #322): publish
  `$JS.core.API.STREAM.INFO` of the two streams, consumer create on
  `KV_delivery` filtered to `$KV.delivery.desired.<edge>.*`, consumer
  create on the objects' chunks, direct get of the objects' meta, consumer
  delete, and `$JS.core.API.$KV.delivery.actual.<edge>.*`; subscribe
  `_INBOX_<edge>.>`. The edge's client uses the inbox prefix
  `_INBOX_<edge>` (natsbus.EdgeInbox), so its user is not given
  `_INBOX.>`, which would let it read every reply in the account.
- **/modules** shows an edge module as "on the edge `<edge>`" with what the
  edge says it runs, that it has not taken the active version yet, or that
  it refused it and why.

## Security

- **Trust boundaries:** the core's bucket and Object Store to the edge's
  module host; the edge's actual value to the core's `/modules`.
- **What an attacker controls:** a compromised edge (its desired reads, its
  actual writes, its own store); anyone who could write an object (they
  cannot: only the core's user may).
- **Abuse cases and what stops it:**
  - an edge reading another edge's desired keys: the consumer create is
    allowed only with the filter `desired.<edge>.*` (server refusal,
    `TestEdgeDeliveryAbuse`); the converger also drops a key of another
    edge (`TestEdgeRefusesWhatItCannotTrust`);
  - an edge writing a desired key, a module's file or another edge's
    actual key: not in its permissions; refused by the server;
  - a substituted or tampered `.wasm` or manifest: the edge computes the
    digest over both files and runs nothing that does not match the key,
    which only the core writes; the Object Store's own digest is checked as
    well, but is not what the edge relies on;
  - a desired value moved under another module's key, or with a digest or
    version that is not one: refused before anything is fetched;
  - huge files: 16 MiB per file, checked from the object's size before
    reading and while reading;
  - a version that fails its health or its init: not run, or rolled back;
    the version before keeps running;
  - an edge's actual text in `/modules`: the version must be a number, the
    digests hex, the result one of three, the reason cut to 200 characters
    and escaped;
  - reading replies meant for others: the edge's inbox prefix and its
    subscribe permission are its own.
- Module signing with the owner's key stays in spec 9.3 for later: the
  digest is trusted because only the core writes `desired`.

## Consequences

- An edge module updates with the install and rolls back with
  `/modules rollback`, as a core module does; the edge switches within a
  second when linked, or when its link returns.
- The core's JetStream domain is `core`; nothing changes for the core's own
  client. An operator's external server must set the same domain before
  edges can take deliveries.
- The core's check before an install loads an edge module but does not run
  its health; a build whose health fails on the edge is reported as refused
  in `/modules`, and the core's store still calls it active.
- A delivery that fails (the bus down) is logged, shown in `/modules`, and
  tried again at the next reload or start.

## Revisit when

Module signing lands (spec 9.3), an edge needs modules from more than one
core, or the core should roll back its own store when an edge refuses a
version.
