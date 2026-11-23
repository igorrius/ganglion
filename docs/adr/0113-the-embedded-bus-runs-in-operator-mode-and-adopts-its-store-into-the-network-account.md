# ADR-0113: Run the embedded bus in operator mode from the vault's chain, move the existing store into the network account, and make an external server's chain opt-in

- **Status:** Accepted
- **Date:** 2026-11-23
- **Deciders:** Igor Artasevych
- **Spec:** [9.3](../spec.md#93-network-security-p2) and R-EDGE-1; builds on [ADR-0109](0109-a-trust-chain-in-the-vault-and-edges-joined-by-a-one-time-code.md), [ADR-0106](0106-embed-nats-with-jetstream-as-the-bus-behind-narrow-interfaces.md), [ADR-0077](0077-encrypted-secret-store-on-the-nodes-key.md)

## Context

ADR-0109 fixed the shape of the trust chain (operator, account `NET` with a
signing key, system account, users; seeds in the secret store). Building it
leaves four details that decision did not settle:

1. A NATS server in operator mode keeps JetStream data per account. Every
   node that runs today stores its streams and the `cells` bucket in the
   global account `$G`, on disk under `<bus>/jetstream/$G`. Switching the
   embedded server to operator mode would make that data invisible.
2. What the node does when the secret store is locked (a master key that does
   not fit), when the chain cannot be read.
3. Whether an external server, which may run with no authentication today
   (M8), is switched to the chain.
4. Which keys are stored and which are made when needed.

## Options

### Existing store

- **A1.** Move `jetstream/$G` to the folder of account `NET` at the first
  start in operator mode (a rename, before the server starts).
- **A2.** Leave the data under `$G` and import it into `NET` through an
  export and import of every stream.
- **A3.** Keep `$G` as the node's account and put only edges in `NET`.

### A locked store

- **B1.** Refuse to open the bus: the node does not start.
- **B2.** Open the embedded bus without a chain (as before M10), move
  nothing, and show a failing `/status` line.

### An external server

- **C1.** The chain is used whenever the bus is external.
- **C2.** The chain is used when the host sets `bus.trust: chain`;
  `bus.credentials` keeps meaning "the operator manages the identity".

## Comparison

| Criterion | A1 | A2 | A3 |
|---|---|---|---|
| Data kept | all, by rename | all, by replay (slow, can fail half way) | all |
| One account for the network (ADR-0109 B1) | yes | yes | no: edges and the core would not share subjects |
| Moving parts | one `rename` | per-stream copy | none, but breaks R-EDGE-2 |

| Criterion | B1 | B2 |
|---|---|---|
| The owner can still fix it from the chat (`/secrets reset`) | no: the node is down | yes |
| Bus data written while locked | none | lands in `$G`; when the right key is back, the start finds both folders and stops, naming them |
| Edges while locked | cannot connect | cannot connect: no listener is opened without a chain |

| Criterion | C1 | C2 |
|---|---|---|
| Breaks an M8 external setup without credentials | yes | no |
| Surprise | an unauthenticated server suddenly needs an operator | the host opts in |

## Decision

A1, B2, C2. B1 would take away the chat, the one place a locked store is
fixed from; B2 keeps the node up and loses nothing silently.

- **Embedded:** always in operator mode: `TrustedKeys` is the operator's
  public key, the account resolver is a memory resolver preloaded with the
  claims of `NET` (JetStream on, the signing key listed, revocations) and of
  the system account, and the node connects in-process as the core's user.
  The server still opens no port; the WebSocket listener comes with the edge
  transport.
- **Store move:** at start, when `<store>/jetstream/$G` exists and the folder
  of `NET` does not, the folder is renamed. If both exist the start fails
  with a message naming both (nothing is merged, nothing deleted). A node
  with no stored data starts empty as before.
- **Locked store:** the embedded server starts as before M10 (no chain, no
  move), and `/status` shows a failing line. The edge transport must not open
  a listener without a chain. An external server with `bus.trust: chain`
  cannot be reached without the chain, so the start fails there.
- **External:** with `bus.trust: chain`, no `bus.credentials` and a server that
  trusts the node's operator and runs `resolver: full`, the node connects
  with a system-account user it makes for the purpose (not stored), pushes
  the claims of `NET` and of the system account, and then connects as the
  core's user. `Chain.OperatorJWT` gives the text the server's owner puts in
  its configuration once. `bus.trust` and `bus.credentials` are mutually
  exclusive.
- **Keys:** the five seeds are stored together, as one JSON entry
  `bus.chain` in the node's scope, written once when the store has no chain,
  so a chain is never stored half made; a chain missing a seed stops the
  start and is never made again over the old one. The revocation list is the
  JSON entry `bus.revoked`. A process beside a running node (`ganglion bus
  trust` while the database is locked) only reads the chain.
- **Reserved names:** `bus.*` in the node's scope belongs to the node. The
  store's ordinary methods (read, set, remove, remove the node's scope, list)
  refuse or hide it, so neither `/secrets rm`, `secret.request`, `ganglion
  secret`, a `store:` reference in the file, nor a module reaches it; only
  the store's internal view, which the chain uses, does. `/secrets reset`
  still drops it, with every other secret: that is the owner's explicit
  start-over.
- **Publishing claims:** one lock is held from a change of the revocation
  list until the server has the new claims, so concurrent revocations never
  hand the server an older list after a newer one.
- The system user for pushes and every edge's user are made from
  a key pair that the issuer does not keep.
- **Claims:** a user JWT is signed by the signing key and says the account it
  belongs to (`IssuerAccount`), so the account's identity key is used only to
  sign the account claims. Every user JWT has an expiry. An account JWT lists
  revoked user keys with the time of revocation.

## Security

- **Trust boundaries:** the secret store (seeds at rest) to the bus
  configuration (keys in memory); the node to an external server (account
  claims pushed under the system account).
- **What an attacker controls:** a modified or copied database file without
  the master key; the network to an external server; a user JWT in transit
  or at rest on an edge.
- **Abuse cases and what stops them:**
  - a user JWT signed by another key, or issued for another account: refused
    by the server (the issuer is not the account's signing key);
  - an expired JWT: refused;
  - a revoked user key: refused and dropped, because the account claims carry
    the revocation;
  - a JWT with permissions wider than the chain meant: not possible to widen
    after issue, since permissions are inside the signed claims;
  - a connection without credentials: refused;
  - seeds read from a copy of the database: sealed with AES-256-GCM under the
    master key (ADR-0077);
  - two stores present at the move: the start fails and nothing is touched;
  - the agent, a chat command or a module deleting or overwriting a seed or
    the revocation list (which would make a new chain or un-revoke every
    edge): refused by the reserved names in the store itself;
  - a locked store: no chain, no listener, a failing `/status` line; the
    in-process server is reachable by the node alone, as before M10.

## Consequences

- Every node's embedded bus changes its account at the first start after
  the update; the cells and streams move with it.
- `secret reset` generates a new chain at the next start. The old account's
  folder stays on disk, unreachable; the cells come back from the backup's
  export. Edges rejoin.
- The user guide gains `bus.trust`.
- The edge transport, join, revoke and desired state build on `Chain`
  (#320–#323).

## Revisit when

Several networks share one server (accounts per edge, ADR-0109 B2), or the
move of the store is found to lose data on a platform whose volume does not
rename atomically.
