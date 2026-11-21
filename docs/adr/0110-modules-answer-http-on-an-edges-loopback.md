# ADR-0110: Let a module answer HTTP on an edge's loopback listener, with the node owning the listener and the headers

- **Status:** Accepted
- **Date:** 2026-11-21
- **Deciders:** Igor Artasevych
- **Spec:** [3](../spec.md#3-deliberately-not-in-0x), [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-13, R-RUN-14), [9.9](../spec.md#99-p2-first-milestones-the-bus-ports-edges-and-http-modules) (R-HTTP-1…R-HTTP-5); allowed in 0.x by [ADR-0105](0105-start-p2-with-the-bus-ports-edges-and-http-modules.md); [design](../design/p2-bus-and-edge-design.md) section 8

## Context

The first scenario is a page on the owner's computer with a battery's readings and notices. The
owner wants the page itself to be a module, so that tomorrow's page, API or collector is the same
kind of thing: a WASM module with a manifest, built by the agent, approved, updated over the air.

## Options

### A. An `http` trigger: the edge's listener starts the module per request

The manifest declares routes; the node owns a loopback listener, gives each module a prefix, starts
the module per request with a bounded request, and adds its own security headers to the answer.

### B. A page built into the ganglion binary

The edge serves a fixed page of cells and notices.

### C. Both

A built-in service page of the edge plus module pages.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Tests "modules as services" | yes | no | yes |
| Tomorrow's page or API is the same thing | yes | a new feature each time | yes |
| Work in M11 | the trigger and a template | a page | both |

## Decision

A (owner, 2026-11-21).

- **Trigger:** `http: { routes: [{path, methods}] }`; each module gets `http://127.0.0.1:8090/<module>/`
  and cannot claim another's prefix; `/` lists the module pages.
- **Request:** method, path inside the prefix, query, body up to 1 MiB, an allowlist of headers
  (`accept`, `content-type`, `if-none-match`). **Answer:** status, an allowlist of headers
  (`content-type`, `cache-control`, `etag`), body. At most 4 requests at once per module; the timeout
  is the module's limit.
- **The node owns:** the bind to `127.0.0.1`; a refusal of any `Host` that is not `127.0.0.1` or
  `localhost` with the listener's port; `Content-Security-Policy: default-src 'self'` and
  `X-Content-Type-Options: nosniff`; dropping `Set-Cookie` and any header outside the allowlist.
- **Data:** current values through `cell_get`; history kept by the page module in its own `state`
  from events it `accepts` (no new history function); notices through a link from
  `<module>.notify`; refresh by polling.
- **Template `web`** in Rust beside `reader` and `reflex`: a skeleton the agent copies for a request.

## Security

- **Trust boundary:** a browser on the edge's machine reaching a module; a module's output reaching
  the browser.
- **What an attacker controls:** any web page the owner visits (it can make the browser send
  requests to loopback); a module's own output.
- **Abuse cases and what stops them:**
  - DNS rebinding from a foreign site: the `Host` check refuses it;
  - a foreign origin reading a page's data: no CORS headers are allowed, so the browser blocks the
    read; the module cannot add them;
  - a module setting cookies or weakening the policy: the node drops them;
  - script injection through a notice's text: the template escapes it, and the policy forbids
    external and inline-injected scripts beyond the module's own;
  - a large or slow request: 1 MiB body cap, 4 at once, the module's timeout;
  - reaching the page from the LAN: the loopback bind; LAN or internet access is Not now and needs
    sign-in.

## Consequences

- The edge role gains `http.local_addr` (default `127.0.0.1:8090`).
- The install window shows a page module's address and its node.
- An API (`api`) and an incoming webhook (`webhook`) later reuse the trigger; access through the core
  reuses calls over the bus (ADR-0108).

## Revisit when

A page needs live push, or has to be reached from another machine.
