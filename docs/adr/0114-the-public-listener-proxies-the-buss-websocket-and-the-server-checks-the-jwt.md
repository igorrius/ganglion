# ADR-0114: Proxy `/bus` from the public listener to the embedded server's loopback WebSocket; the server checks the JWT, the proxy bounds what reaches it

- **Status:** Accepted
- **Date:** 2026-11-23
- **Deciders:** Igor Artasevych
- **Spec:** R-EDGE-4 in [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules); builds on [ADR-0109](0109-a-trust-chain-in-the-vault-and-edges-joined-by-a-one-time-code.md) (D1), [ADR-0113](0113-the-embedded-bus-runs-in-operator-mode-and-adopts-its-store-into-the-network-account.md), [ADR-0078](0078-telegram-updates-by-polling-or-webhook.md)

## Context

An edge's leaf has to reach the core's bus from the internet. ADR-0109 chose
the public listener (no new port, the platform's TLS) with the upgrade proxied
to a NATS WebSocket listener on loopback. Building it leaves open how the
proxy works, where the JWT is checked, and what bounds it.

Facts about `nats-server` 2.15 that shape this:

- Its WebSocket listener serves every path; a path that ends in `/leafnode`
  makes a leaf connection, any other a client. A leaf accepted over WebSocket
  needs the leaf-node listener to be configured (`LeafNode.Port != 0`).
- A leaf puts the user JWT and the nonce signature inside the NATS protocol
  (`CONNECT`), after the upgrade. The HTTP request carries no credential, and
  a leaf cannot add a header or a cookie.
- In operator mode the server refuses a connection with no valid JWT, a JWT
  not signed by the account's signing key, an expired JWT, a revoked key, or a
  wrong nonce signature; `AuthTimeout` closes a connection that sends nothing.
- `MaxPayload` bounds a message and a WebSocket frame; `MaxConn` bounds
  connections.
- The leaf dials `wss://<address>/bus`, so its request path is
  `/bus/leafnode`.

The public listener serves each request under a small in-flight limit and a
write timeout; a tunnel that lives for days fits neither.

## Options

### A. A reverse proxy to the loopback listener; NATS checks the JWT

`httputil.ReverseProxy` forwards `GET /bus` and `GET /bus/leafnode` upgrades to
`127.0.0.1:<ws port>` and copies bytes. The proxy admits only what can be a
WebSocket upgrade and bounds the number of tunnels; NATS does the
authentication.

### B. The proxy checks the JWT before it upgrades

The edge would send its JWT in a header and the proxy would verify it with the
chain. Needs a protocol on top of NATS's own, a header a leaf cannot send, and
a second implementation of NATS's checks (expiry, revocation, issuer, nonce).

### C. A WebSocket library in the proxy that speaks the frames

Terminates the WebSocket in Go and opens a plain NATS connection to the server
for each edge. A new dependency, and our code parses and relays frames.

### D. A second public port with `nats-server`'s own WebSocket TLS listener

No proxy code at all, but a new public port and TLS the platform does not give.
ADR-0109 rejected it (D2).

## Comparison

| Criterion | A | B | C | D |
|---|---|---|---|---|
| New dependency | none (standard library) | none | a WebSocket library | none |
| JWT checks | NATS's, the only implementation | ours beside NATS's | NATS's, behind our relay | NATS's |
| Works for a stock leaf | yes | no: a leaf sends no header | yes | yes |
| Code that touches the stream | none: bytes | little | frames: parsing hostile input | none |
| New public port | no | no | no | yes |
| An unauthenticated peer reaches NATS | yes, for `AuthTimeout` | no | yes, our relay | yes |

## Decision

A. The check of the JWT, the nonce signature, the expiry and the revocation
stays in one place, the server, which is the code the NATS project tests and
the chain (ADR-0113) already feeds. B is not possible with a stock leaf, C
adds a dependency and a parser for hostile frames, D opens a port and has no
TLS. What A leaves is that an unauthenticated peer holds a socket to the
server for the auth timeout; the bounds below make that cheap.

Details:

- **Where.** The listener gets `HandleStream(path, h)`: a route that is not
  counted against the in-flight limit (a tunnel is long) and whose handler
  bounds itself; the server's context ends with the listener, so a stop closes
  the tunnels. The proxy package (`bus/natsbus/wsproxy`) registers
  `/bus/leafnode` (the edge's leaf) and `/bus` (a client with the same user
  JWT and the same permissions) and nothing else under `/bus`.
- **What the proxy admits.** `GET` with `Connection: Upgrade` and `Upgrade:
  websocket`; anything else on these paths gets 400/405 and never reaches the
  server. The path is rewritten to `/leafnode` or `/`. Forwarding headers are
  dropped (`X-Forwarded-*`, `Forwarded`, cookies, `Authorization`): the
  server needs none and the loopback listener is configured with no cookie
  or origin trust.
- **Fail closed.** The WebSocket listener exists only when the embedded
  server has a chain: with a locked store, an external server or no chain
  there is no listener, no route, and `/bus` is a 404 like any unknown path.
  The proxy dials the address the bus gave it and nothing else; if the dial
  fails the answer is 502 and nothing is retried.
- **Limits** (the values are the builder's, in `wsproxy.DefaultConfig`):
  - tunnels at once: 64; one more gets 503. The server's `MaxConn` is set
    above that so the server's own limit is a backstop, not the first line;
  - `MaxPayload` 1 MiB (explicit, not the default of a version);
  - `AuthTimeout` 2 s and `HandshakeTimeout` 5 s on the loopback listener, so
    a socket that does not authenticate is closed by the server;
  - subscriptions per user stay in the user's JWT (#322 sets them with the
    permissions).
- **No per-address limit.** Behind the platform's proxy every connection
  comes from the platform; trusting a forwarded address is a decision that
  needs the platform's guarantee, and without it the cap on tunnels is the
  honest bound.
- **No TLS on the loopback hop**: `NoTLS`, bound to `127.0.0.1`; TLS ends at
  the platform (ADR-0078). The leaf-node TCP listener a leaf over WebSocket
  needs is bound to loopback too and requires the same JWT.

## Security

- **Trust boundaries:** the internet to the public listener (existing); the
  public listener to the embedded server on loopback (new: a tunnel of raw
  bytes). The server's authentication is the boundary.
- **What an attacker controls:** any bytes on the public address, the number
  and timing of connections, the size of what it sends, a stolen or old JWT.
- **Abuse cases and what stops them** (each has a test that reads the refusal):
  - no JWT: the server refuses `CONNECT` ("authorization violation") and a
    socket that sends nothing is closed at the auth timeout;
  - an expired JWT: refused by the server;
  - a revoked user key: refused, and a connection already open is dropped;
  - a JWT signed by another key, or for another account: refused (not the
    account's signing key);
  - a valid JWT with a wrong nonce signature (a copied JWT without the
    seed): refused;
  - an oversized message or frame: the server closes the connection
    (`MaxPayload`);
  - a flood of connections: the 65th gets 503 from the proxy and costs the
    server nothing; sockets that never authenticate are freed after the auth
    timeout, so the cap does not stay held;
  - a request that is not an upgrade, a wrong method or a path under `/bus`
    other than the two: refused by the proxy;
  - a header that tries to set a client's identity, origin or address:
    dropped by the proxy;
  - a locked store or no chain: no listener, no route.
- **Left open:** an authenticated edge can hold a tunnel; one edge's use of the
  64 is not capped per user. With few edges this is acceptable; a per-user cap
  needs the user's identity, which only the server knows, so it is a
  `MaxConnections` limit inside the user's JWT (#322).

## Consequences

- An edge needs only the public address and its JWT: no port, no extra TLS.
- The core's availability to its own users is protected from a flood by the
  cap; the cost is that 64 hostile sockets can crowd edges out for the auth
  timeout at a time. Revisit if that is seen.
- `ganglion bus` has nothing to change; the loopback WebSocket and leaf
  listeners are internal.
- The user guide gains a line saying that `/bus` on the public address is the
  edges' entry and what it refuses.

## Revisit when

An address is trustworthy behind the platform (a per-address cap), many edges
make 64 tunnels too few, or the server gains an authentication step that
runs before the upgrade.
