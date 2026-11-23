# ADR-0118: Refuse what another site makes a browser send, and fix the rest of the loopback listener's contract in the node

- **Status:** Accepted
- **Date:** 2026-11-23
- **Deciders:** Igor Artasevych
- **Spec:** [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules) (R-HTTP-1…R-HTTP-3, R-EDGE-7); refines [ADR-0110](0110-modules-answer-http-on-an-edges-loopback.md); [design](../design/p2-bus-and-edge-design.md) section 8

## Context

[ADR-0110](0110-modules-answer-http-on-an-edges-loopback.md) decided that a module answers HTTP on
an edge's loopback listener, with the node owning the bind, the `Host` check and the headers. It
left open what #326 has to decide to build it:

- **Writes from another site.** The `Host` check stops DNS rebinding, and the absence of CORS
  headers stops another site from *reading* a page. Neither stops another site from *sending*: a
  page the owner visits can submit a form or `fetch(..., {mode: "no-cors"})` a `POST` to
  `http://127.0.0.1:8090/<module>/api`, with the right `Host`, and the module runs it. It can also
  embed a module's route as a `<script>` or an `<img>`, which a browser loads across origins.
- **The policy's other directives.** `default-src 'self'` does not cover framing, `<base>` or form
  targets; a page framed by another site can be clicked through.
- **The wire shapes** of R-HTTP-2 name the fields, not their JSON; the `web` template (#327) is
  written against them.
- **Statuses and limits** for each refusal, the size of an answer, how many requests the listener
  takes in all, and how a page's failures count, since a page polls every few seconds.

## Options

### A. Refuse cross-site requests by `Origin` and Fetch Metadata, in the node

The listener refuses a request whose `Origin` is not its own address, a write whose
`Sec-Fetch-Site` is not `same-origin` or `none`, and a cross-site read that is not a top-level
navigation (a script, an image, a fetch). The owner still opens a page from a link elsewhere: that
is a navigation. The node adds `frame-ancestors 'none'` and its sibling headers to the policy.

### B. Leave writes to each module

The node keeps to ADR-0110's list; a module that changes state on `POST` checks a token of its own,
which the template would carry.

### C. A token per page, issued by the node

The node sets a token in each page and requires it on every write (a double-submit or a
synchroniser token).

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Stops another site's writes | yes, in every browser that sends `Origin` on writes (all current ones) | only in modules that remember | yes |
| Stops cross-site embedding of a route | yes (Fetch Metadata and `Cross-Origin-Resource-Policy`) | no | no |
| The module's or the template's work | none | a token in every page module | the template reads and sends the token |
| The node's work | a few header checks | none | a store of tokens, cookies or page rewriting, which ADR-0110 forbids (no cookies) |

## Decision

A. The node is the only place every page module shares, the check is a few lines with no state,
and it needs no cookie, which ADR-0110 rules out.

The rest of the contract, fixed with it:

- **Order of the checks** (each a real refusal with its status): at most 32 requests in flight on
  the listener (503, `Retry-After: 1`); `Host` is the listener's own address, `127.0.0.1:<port>`,
  `localhost:<port>` or `[::1]:<port>` when bound there, compared without case (403); the
  cross-site rules of A (403); a path with an encoded `/` or `\`, a `\`, a control byte, or a dot or
  empty segment before its end (400); a body over 1 MiB, by its length or as read (413). The module
  host then refuses a prefix or route that does not run or exist (404), a method the route does not
  list (405 with `Allow`), and a fifth request of a module (429, `Retry-After: 1`).
- **Headers the node sets** on every answer, refusals included:
  `Content-Security-Policy: default-src 'self'; base-uri 'none'; form-action 'self'; frame-ancestors 'none'`,
  `X-Content-Type-Options: nosniff`, `Cross-Origin-Resource-Policy: same-origin`,
  `Cross-Origin-Opener-Policy: same-origin`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`.
  A module's headers pass only through the allowlist of ADR-0110 (`content-type`,
  `cache-control`, `etag`, any case, printable ASCII up to 1 KiB); an answer without a
  `content-type` is `application/octet-stream`.
- **Routes** match exactly, with no prefix or wildcard: a module is asked only for a path its
  manifest lists, and the install window shows each one. A path is `/[A-Za-z0-9._~/-]*`, at most
  128 bytes, with no dot or empty segment but a trailing slash; at most 32 routes; methods `GET`,
  `POST`, `PUT`, `PATCH`, `DELETE`. `HEAD` and `OPTIONS` are not a module's: the listener answers
  405.
- **Wire shapes** (R-HTTP-2): the payload is
  `{"method", "path", "query", "headers", "body"}` and the result
  `{"status", "headers", "body"}`; `query` is the raw query without `?`, `body` is base64 as JSON
  encodes bytes (the shape `http_fetch` already uses), unknown fields in the result are a break of
  the contract. A status outside 200..599 or a body over 4 MiB is one too.
- **A page's runs.** "4 at once" counts the run and those waiting for it (the runner runs a
  module's calls one at a time). A run does not end when the browser goes away: a run cut short
  would count as a failure, and a page that polls would turn its module off; the module's time
  limit bounds it. A failed run answers 502, or 504 past the time limit, in the node's words; it
  counts toward the crash loop like any run, is not escalated to the agent (the browser saw it),
  and is the only kind of http run written to the run journal.
- **Where it runs.** The listener is a component of any node with the edge role, an edge alone or
  beside the core; `http.local_addr` takes `127.0.0.1`, `::1` or `localhost` (any case) with a
  port, or `off`: another loopback address would bind and then refuse every `Host`. A port it
  cannot bind stops the node.
- **The install window** names the prefix `/<module>/` and the routes, and says the host and port
  are the `http.local_addr` of the node that runs the module: the node that asks does not know an
  edge's setting.

## Security

- **Trust boundaries:** a browser on the edge's machine and every site it visits, to the
  listener; a module's answer, to the browser.
- **What an attacker controls:** any web page the owner visits (it makes the browser send
  requests to loopback with the listener's `Host`, and names its own `Origin`); the bytes of a
  request line and its headers, from any local process; a module's answer.
- **Abuse cases and what stops them:**
  - another site posts to a module's route: the `Origin` check, and Fetch Metadata for a browser
    that omits `Origin`;
  - another site embeds a route as a script or an image to read it: the Fetch Metadata check and
    `Cross-Origin-Resource-Policy: same-origin`;
  - another site frames a page to click through it: `frame-ancestors 'none'` and
    `X-Frame-Options: DENY`;
  - a path that climbs into another module's prefix (`..`, `%2e%2e`, `%2f`, `\`): refused before
    any module is chosen; the prefix is the first segment, matched exactly;
  - a request line in absolute form naming a foreign host: Go takes that host as the request's,
    and the `Host` check refuses it;
  - a module that injects headers (a line break in `etag`, `Set-Cookie`, CORS, `Location`): the
    allowlist and the value check drop them;
  - a flood of requests: 32 in flight on the listener, 4 per module, 1 MiB per body, the module's
    time limit, and the server's read and header timeouts.

## Consequences

- A page module needs no token or cookie of its own; a write from its own page works because the
  browser sends the page's own `Origin`.
- A page cannot be embedded in another site or open one in a frame, and a link from another site
  opens it but cannot post to it.
- A browser that sends neither `Origin` nor `Sec-Fetch-Site` passes the cross-site check on a
  `GET`, so a template or a module never changes state on `GET`: writes go on `POST`, `PUT`,
  `PATCH` or `DELETE`, which every current browser marks with `Origin`.
- The `web` template serves its scripts and styles as routes of their own: the policy refuses
  inline ones.
- A module's trial run cannot yet pull an http trigger; `examples.yaml` steps for it are a
  follow-up.

## Revisit when

A page has to be reached from another machine, or a browser the owner uses sends neither `Origin`
nor Fetch Metadata on a write.
