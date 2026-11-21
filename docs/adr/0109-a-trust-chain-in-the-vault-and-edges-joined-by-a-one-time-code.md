# ADR-0109: Keep the NATS trust chain in the secret store, give each edge its own permissions in one account, and join an edge by a one-time code with its key born on the edge

- **Status:** Accepted
- **Date:** 2026-11-21
- **Deciders:** Igor Artasevych
- **Spec:** [7.8](../spec.md#78-security-and-operations-mvp) (R-BAK-1), [7.14](../spec.md#714-node-configuration) (R-CFG-9, R-CFG-10, R-CFG-11), [9.2](../spec.md#92-module-updates-desired-state-p2), [9.3](../spec.md#93-network-security-p2), [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules) (R-EDGE-1…R-EDGE-7); builds on [ADR-0077](0077-encrypted-secret-store-on-the-nodes-key.md), [ADR-0078](0078-telegram-updates-by-polling-or-webhook.md); [design](../design/p2-bus-and-edge-design.md) section 7

## Context

Edges (ADR-0105) join the core's bus from other machines. The owner once set up NATS security by
hand and stopped at one question: where the keys come from after the system is restored from a
backup. The node already keeps an encrypted secret store opened by a master key the owner keeps off
the node (ADR-0077), and daily encrypted backups (ADR-0095). The core runs on a platform with one
public HTTPS address (ADR-0078); the owner's computer is the first edge.

## Options

### Where the trust chain lives

- **A1.** Operator, account, signing and core user seeds in the secret store; they ride in the
  backup and open with the master key.
- **A2.** Seeds in files on the volume beside the database.

### How permissions are split

- **B1.** One account `NET` for the network; each edge a user with permissions derived from its
  modules' links.
- **B2.** An account per edge, with exports and imports between accounts.

### How an edge gets its credentials

- **C1.** A one-time code: the owner approves in the chat, gets a 10-minute code; the edge generates
  its NKey and sends only the public key with the code over HTTPS; the core returns a signed JWT.
- **C2.** The core issues a `.creds` file and sends it to the chat as a document.
- **C3.** A command on the host makes the `.creds` file, carried over by hand.

### How an edge reaches the core

- **D1.** WebSocket under `/bus` on the existing public listener, proxied to a loopback NATS
  WebSocket port.
- **D2.** A second public port or domain for NATS.

## Comparison

| Criterion | A1 | A2 |
|---|---|---|
| Survives a restore with only the master key | yes | yes, but seeds sit in the clear on the volume |
| Seeds at rest | encrypted | plain |

| Criterion | B1 | B2 |
|---|---|---|
| Isolation | per-user permissions | per-account subjects |
| Complexity | low | exports and imports per edge |

| Criterion | C1 | C2 | C3 |
|---|---|---|---|
| The private key leaves the edge | never | in the chat's history | by hand |
| Through the agent, from the chat | yes | yes | no |

| Criterion | D1 | D2 |
|---|---|---|
| New public port | no | yes |
| TLS | the platform's, as for the webhook | needs its own |

## Decision

A1, B1, C1, D1 (owner, 2026-11-21).

- **Chain:** operator → account `NET` (with a separate signing key) and the system account → users
  (the core and each edge). Seeds in the secret store; a restore brings the chain back with the
  master key; edges keep working after a restore of the core.
- **Embedded server:** its configuration is built at start from the chain (operator JWT, a memory
  resolver preloaded with the accounts). **External server:** given the operator JWT once, runs
  `resolver: full`; the node pushes account JWTs through the system account.
- **Permissions:** an edge publishes only its own subjects (`g.ev.<edge>.>`, `g.tel.<edge>`, its
  modules' call answers, its modules' cell keys, `actual.<edge>.>`) and subscribes only to what its
  links need, `desired.<edge>.>` and its assigned `modules` objects. The node derives them from the
  links and re-issues the edge's JWT when they change. Only the core writes `desired` and `modules`.
- **Join:** the owner asks in the chat; a card shows the name and permissions; Approve gives a
  one-time code (128 bits, stored hashed, single use, 10 minutes, bound to the name, attempts rate
  limited); `ganglion edge join <url> <code>` on the edge generates the NKey, posts the code and the
  public key over HTTPS, and receives the user JWT, the operator public key and the bus URL; the owner
  is told "`<edge>` joined".
- **Transport:** the leaf dials `wss://<public address>/bus`; the public listener proxies the upgrade
  to the embedded server's WebSocket listener on `127.0.0.1` (`no_tls`, TLS ends at the platform). An
  external server gets the edge's URL from `bus.urls` in the join answer.
- **Revocation:** a Revoke button adds the edge's public key to the account JWT's revocations; the
  server drops and refuses it. A lost machine is revoked and a new one joined.
- **Modules on an edge:** delivered by desired state (`desired.<edge>.<module>` with version and
  SHA-256, the `modules` Object Store, `actual.<edge>.<module>` back). Module signing with the owner's
  key (spec 9.3) stays for later.

## Security

- **Trust boundaries:** an edge joining the network; an edge's messages entering the core; the core's
  modules and desired state entering an edge.
- **What an attacker controls:** the network; a leaked join code; a stolen or compromised edge.
- **Abuse cases and what stops them:**
  - an anonymous connection to `/bus`: refused by NATS without a valid JWT and nonce signature;
  - a leaked code: single use, 10 minutes, rate limited, the owner told of every join, Revoke;
  - a stolen credentials file: Revoke;
  - an edge writing another module's cell, event or desired state: refused by its JWT permissions;
  - a substituted module on an edge: the hash in `desired`, which only the core writes;
  - losing the chain with the volume: seeds in the backup, opened by the master key kept off the
    node.

## Consequences

- The secret store gains node-scope entries for the chain; they are never shown, like any secret.
- The public listener gains a WebSocket proxy route.
- `/status` gains a line per edge; the chat gains join and revoke flows.

## Revisit when

Several networks share one server, or an edge needs to be isolated from others more strictly than
user permissions allow; then accounts per edge (B2).
