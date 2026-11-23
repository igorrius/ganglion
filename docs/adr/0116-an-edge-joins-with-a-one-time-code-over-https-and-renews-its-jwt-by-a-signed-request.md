# ADR-0116: An edge joins with a one-time code over HTTPS and renews its JWT by a signed request

- **Status:** Accepted
- **Date:** 2026-11-23
- **Deciders:** Igor Artasevych
- **Spec:** R-EDGE-2, R-EDGE-3, R-EDGE-5; builds on [ADR-0109](0109-a-trust-chain-in-the-vault-and-edges-joined-by-a-one-time-code.md), [ADR-0113](0113-the-embedded-bus-runs-in-operator-mode-and-adopts-its-store-into-the-network-account.md), [ADR-0114](0114-the-public-listener-proxies-the-buss-websocket-and-the-server-checks-the-jwt.md), [ADR-0115](0115-an-edge-keeps-its-events-in-its-own-domain-and-the-core-sources-them.md); closes #369

## Context

ADR-0109 fixed the shape of the join: the owner asks in the chat, approves a
card, gets a one-time code, runs `ganglion edge join <url> <code>` on the
edge. It left the details to the code. Building it needs these:

1. The code's format.
2. How the edge's request reaches the core, and how it is limited.
3. Where codes and joined edges are kept.
4. How an edge gets a new JWT when its permissions change or its JWT runs
   out (R-EDGE-2 says the node re-issues it).
5. How narrow the edge's permissions can be made: #369 found that the
   shared `$JS.S.>` and `$JSC.R.>` let one edge write into another's source
   deliveries, and that `changeSources` is not safe for several callers.

Measured with nats-server 2.15.3: with `ExternalStream.DeliverPrefix`
`g.src.<edge>` the core's source asks the edge to deliver to
`g.src.<edge>.S.<id>`, so an edge needs to publish only that prefix. The
`$JSC.R.<id>` replies to the core's consumer requests are still published by
the edge's server over the leaf link; `allow_responses` does not cover them
(the copy stopped when `$JSC.R.>` was removed), so the wildcard stays.

## Options

### Code format

- **A1.** 16 random bytes from `crypto/rand`, written as 26 characters of
  Crockford base32 in groups of four (`ABCD-EF01-...`); typed or pasted.
- **A2.** A link with the code inside (`https://host/join#<code>`), opened
  on the edge.
- **A3.** Words from a list (like a passphrase); 12 words for 128 bits.

### Transport of the join

- **B1.** `POST /edge/join` on the public listener (R-CFG-11), JSON, over the
  platform's TLS; a second route `POST /edge/renew`.
- **B2.** Over NATS with a bootstrap user that may only publish to a join
  subject.

### Where codes and edges are kept

- **C1.** Codes in memory only (hashed); edges in the secret store as
  `bus.edges` (reserved, node scope).
- **C2.** Both in the secret store.
- **C3.** Edges in SQLite beside the journal.

### New JWT

- **D1.** The edge joins again with a new code whenever its JWT runs out.
- **D2.** The edge asks `POST /edge/renew` with a request signed by its own
  NKey; the core answers with a JWT derived from the current permissions.
- **D3.** A 90-day JWT and no renewal; a permission change waits for it.

### Limiting attempts

- **E1.** A limit on join attempts for the whole listener: a burst of 5,
  one more every 12 s, each attempt taking a token up front and a successful
  join giving it back; past it the answer is 429 without looking at the
  code. Renewals are not under it.
- **E2.** A limit per remote address.

## Comparison

| Criterion | A1 | A2 | A3 |
|---|---|---|---|
| Entropy | 128 bits | 128 bits | 128 bits |
| Pasting into a shell | one token | the shell may treat `#` and `?` | many words |
| Leaks in a browser's history or a proxy's log | no | possible | no |

| Criterion | B1 | B2 |
|---|---|---|
| Needs an unauthenticated NATS user | no | yes: a second way in to the bus |
| Reuses the listener, its limits and the platform's TLS | yes | no |

| Criterion | C1 | C2 | C3 |
|---|---|---|---|
| A restart loses codes | yes (the owner asks again; they live 10 minutes) | no | no |
| Backup carries the edges | yes, with the secrets | yes | needs its own migration |
| Hashes of live codes on disk | none | yes | yes |

| Criterion | D1 | D2 | D3 |
|---|---|---|---|
| Owner's work | a card every renewal | none | none |
| Permission change reaches the edge | at the next join | within the renew interval | after up to 90 days |
| A stolen JWT without the seed | useless | useless | useless |

| Criterion | E1 | E2 |
|---|---|---|
| Works behind the platform's proxy (one address for all) | yes | the address is the proxy's |
| A stranger can block joins | while the stranger keeps failing, and about a minute after | the same, from behind one proxy address |

## Decision

A1, B1, C1, D2, E1.

- **Code:** 128 bits, shown once in the chat, bound to the edge's name and to
  the owner who approved it. Stored as SHA-256 in memory with an expiry of 10
  minutes; it is spent at the first use with a usable key, whatever comes after; a wrong code, a bad key or a key a joined edge holds spends nothing. At most 8
  codes are live; asking for another for the same name replaces the first.
- **Join:** `POST /edge/join` with `{code, public_key}`. The core hashes the
  code, finds it in constant time, checks the expiry, that the name is
  still free, that no joined edge holds the key, and that the permissions
  are still those the card showed (their digest is bound to the code; a
  change of the links in between refuses the join with 409), then issues a 24-hour user JWT for the key. The answer holds
  the JWT, the operator public key, the bus URL (when the public address is
  known) and the edge's name. The seed is made on the edge and never sent.
  The owner is told "`<edge>` joined" in the chat; the notice carries the
  name and the time, never the key's seed or the code.
- **Renew:** `POST /edge/renew` with `{public_key, at, signature}`, the
  signature over `renew <public_key> <at>` by the key; `at` within 60 s of
  the core's clock. The core answers only for a key in its list of joined
  edges that is not revoked, with a JWT derived from the permissions as they
  are now. A replayed request returns a JWT only to someone without the seed.
  The edge renews every hour and restarts its leaf link when the
  permissions in the new JWT differ.
- **Edges:** the secret store entry `bus.edges` holds, for each edge,
  name, public key, joined time and a permission digest. The revocation list
  `bus.revoked` stays the authority for refusal. A name is held by one
  active edge; a revoked edge's name is free again.
- **Permissions:** `natsbus.EdgePermissions(edge, needs)` is a pure function.
  The base is: publish `g.src.<edge>.>`, `$JSC.R.>`, `g.tel.<edge>`;
  subscribe `$JS.<edge>.API.>`. Each link whose one end is a module on the
  edge and the other a module on the core adds: for a call from the edge,
  publish `g.rpc.core.<module>.<port>`; for a call to the edge, subscribe
  `g.rpc.<edge>.<module>.<port>`. Nothing else is
  derived. The source of an edge uses `DeliverPrefix` `g.src.<edge>`, so an
  edge cannot write into another's deliveries (#369). Desired state and
  module objects, which only the core writes, are added by the work that
  delivers them (#323) through `needs`.
- **Source changes:** `Bus` serialises `changeSources` and `ensureStream`
  behind one lock (#369).
- **Revoke:** the Revoke button calls `Bus.Revoke` for the key, then
  `UnsourceEdge`, then removes the edge from `bus.edges` after both succeed
  and shows the result. A press of Revoke on an edge that is gone is a no-op.
- **Limit:** `/edge/join` has the limiter (E1); a limited request gets 429
  and is not looked at. `/edge/renew` is not limited by it: its signature
  is checked first and needs the seed, so there is nothing to guess, and a
  stranger's bad renewals must not hold back a joined edge's renewal, which
  would let its JWT run out. The listener's own limits bound the work.

## Security

- **Trust boundaries:** the public listener's two new routes (the network
  to the core); the owner's chat to the core (the card); the edge's data
  directory (the seed).
- **What an attacker controls:** the network, a leaked or guessed code, a
  stolen credentials file, a request replayed from a log, another person in a
  group chat.
- **Abuse cases and what stops them (all with tests):**
  - a code used twice: removed at the first use; the second gets 401;
  - an expired code: 401 at 10 minutes and one second;
  - a wrong or truncated code: 401, counted; past the burst 429;
  - a flood of join attempts: the limiter answers 429 before any work, and
    parallel attempts never pass on one token;
  - a flood of bad renewals: refused at the signature, and a joined edge's
    renewal and the owner's join still work;
  - a code used with permissions the card did not show: refused, 409;
  - a second name under a key a joined edge holds: refused, 409;
  - a code asked for a name that is taken or not a valid edge name: no card,
    no code;
  - a person who is not an admin approving a card or asking for one: refused
    in code, whatever the model says; the buttons are bound to the admin's
    chat and expire in 24 hours;
  - a public key that is not a user key, or the core's own key: refused,
    the code stays unused;
  - a revoked key joining or renewing: refused, 403; its connection is
    dropped by the server (ADR-0113);
  - a renewal signed with another key, or with a stale time: refused;
  - one edge writing another's source deliveries: refused by the server
    (test);
  - a code in a log: codes are never logged, never put in an error, and the
    guide says to treat one like a password for ten minutes.

## Consequences

- Closes #369: scoped source deliveries and one writer of the stream's
  sources.
- An edge's permission change reaches it within an hour, or at once after a
  restart.
- A restart of the core loses unused codes.
- A stranger who keeps failing joins blocks joins for as long as it goes on
  and about a minute after (one token every 12 s); the owner asks again.
  Renewals of joined edges go on regardless. No code is lost to a failed
  try.
- The leaf link keeps the shared `$JSC.R.>` publish: a compromised edge can
  forge an answer to a JetStream request of the core on a subject it can
  only learn by seeing the request, which only its own server's requests
  reach. Measured; not scoped further.

## Revisit when

nats-server scopes a source's reply subjects, an edge needs a second
account, or joins come often enough that a restart losing codes hurts.
