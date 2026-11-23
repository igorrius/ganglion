# web-template

The starting point for a page: a module that serves a small web page on an
edge's loopback listener (spec R-HTTP-4, R-HTTP-5). The agent does not write a
module from nothing; it imports this one under a new name (`module.import`),
changes the files below, builds (`module.build`) and offers the result to the
owner (`module.install`). The `module-writing` skill describes the loop.

The module is started for each request with the trigger `http`; it answers
and ends. It never pushes anything: the page asks again every 15 seconds.

## What the agent changes

- `manifest.yaml`: the name, the routes (`triggers: http`), the cells the page
  reads (`cells.read`), the ports it accepts, the settings (`config`) and the
  limits.
- `src/logic.rs`: what the page keeps and shows: `LISTENED` (the cells it
  reads, the same as `cells.read`), `Settings`, `State`, `on_reading`,
  `on_notice` and `snapshot` (the JSON the page polls), with their tests.
- `assets/index.html`, `assets/app.js`, `assets/app.css`: the page itself.
  They are compiled into the module (`include_str!`); the node serves nothing
  from disk.
- `examples.yaml`.

Everything else stays: `src/lib.rs` is the routing and the state, `src/http.rs`
the request and the answer and the escaping, `src/assets.rs` the files and the
tests that guard them, `src/host.rs` the host as a trait, `src/guest.rs` the ABI
glue for `wasm32`, `src/fake.rs` the test host. `Cargo.lock` lists the only
crates a module may use: `serde` and `serde_json`.

## The example page

One cell, `producer.value`: its current value (read with `cell_get` on every
request), the last 24 hours of it as an inline-SVG chart, and the latest 20
notices.

- **History** is the module's own: it `accepts` a `reading` event (`{value:
  number}`) from a reader through a link and keeps `[time, value]` pairs in
  `state`, newest last, at most `max_points` (default 288) and none older than
  `keep_hours` (default 24). There is no history function in the host.
- **Notices** come through a link from another module's `<module>.notify` to
  the `notices` port (`{text: string, at: string}`). Each is cleaned (control
  characters become spaces, 280 characters at most) and the newest 20 are kept.
- **Routes**: `/` the page, `/app.js`, `/app.css` (fixed files with an `etag`;
  `If-None-Match` gets 304) and `/api/data` (the JSON the page polls). Any other
  path is 404, any method but GET is 405.

## The rules the page lives by

The node serves the page with `Content-Security-Policy: default-src 'self'`,
so the page can load only its own files:

- no inline script, no event-handler attribute, no inline style (a `style`
  attribute or element is blocked too: style with `app.css` and SVG
  attributes), no outside address, no font or image from elsewhere;
- text from outside (a notice, a cell's value, a setting) is never put into
  markup. The page builds its elements and sets `textContent`; the server
  escapes the only dynamic text it writes into HTML (the title) with
  `http::escape_html`, and writes JSON with `<`, `>` and `&` as `\u` escapes
  (`http::json_body`), so even a browser that took it for HTML would see no tag.
- `src/assets.rs` has tests that fail on `innerHTML`, `eval`, an inline script
  or an outside address; keep them passing when you change the page.

## Assumptions about the HTTP shapes

The spec fixes what a request and an answer hold but not the field names. This
template reads the request as `{"trigger": "http", "payload": {"method",
"path", "query", "headers", "body"}}` (`path` is inside the module's prefix,
`query` an object of strings, `headers` an object with lower-case names, `body`
text) and answers `{"ok": true, "result": {"status", "headers", "body"}}`
with `content-type`, `cache-control` and `etag` as headers. All of it is in
`src/http.rs` (`Request::from_payload` and `Response::to_output`), so a change
in the node's shapes is a change in that one file. The page's cells are read
without a cell trigger, with `cells.read` alone.

## Events and the trial run

`examples.yaml` delivers events to the ports; a trial run cannot send an HTTP
request yet, so routing and escaping are covered by `cargo test`.

## Developing

```sh
cargo test --locked      # routing, state, escaping, the guards on the assets
cargo clippy --locked --all-targets -- -D warnings
cargo fmt --check
cargo clippy --locked --target wasm32-wasip1 -- -D warnings
```
