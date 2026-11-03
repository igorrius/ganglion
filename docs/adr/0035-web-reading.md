# ADR-0035: Read pages through an html2md WASM module in Rust; search with Brave or DuckDuckGo

- **Status:** Proposed
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.11](../spec.md#711-reading-the-web) (R-WEB-1–3), [7.5](../spec.md#75-runner-and-wasm-modules) (modules), design section 5.5 ([M3b and M3c design](../plans/p1-m3b-m3c-design.md#55-reading-the-web)); [ADR-0031](0031-agent-security-baseline.md) (the baseline, the URL rule), [ADR-0011](0011-module-abi.md) (the module ABI), [research note](../research/2026-11-02-agent-security-baseline.md)

## Context

M3b's acceptance reads the web for the owner: the hryvnia rates every
hour from the National Bank of Ukraine's JSON API inside a grant on
`bank.gov.ua`, and web pages the owner asks about. Three things need a
decision.

**Turning HTML into text.** The owner decided that this runs in a WASM
module, `html2md`, not in the core: its parser library is updated by
shipping one `.wasm`, without a core release, and hostile HTML is parsed
in the sandbox with no host functions granted (R-WEB-1). Open was the
language. The design asked for a spike of about two hours, Rust (`htmd`
and a readability crate) against TinyGo (`golang.org/x/net/html` and an
own extractor), on five saved pages (a news article, docs, a forum
thread, a table-heavy page, a hostile page with scripts and a 2 MB
inline blob), picked by output quality, `.wasm` ≤ 3 MB, p95 of a call
≤ 150 ms in `tools/modbench`, and no trap on the hostile page.

**Search.** R-WEB-3: one backend behind an interface; the default needs
no key, keyed backends are enabled by a key. OpenClaw's key-free options
are DuckDuckGo's HTML page, which its own docs call "experimental,
unofficial", and a self-hosted SearXNG. The design's call: a scrape that
breaks is a poor default; compare reliability.

**The fetch itself** runs in the core (R-WEB-1): http(s) only, no
private addresses, re-checked after redirects, limits, the result marked
as outside content. Rulings for this task: the address fetched is
exactly the one `internal/urlnorm` produced for the policy's check; every
connection checks the resolved IP; inside a grant a redirect off the
granted hosts is refused; errors are fixed texts, never a response body.

## Options

### Converter

#### A. Rust: own extractor on `dom_query`, Markdown by `htmd`

A byte-level pre-pass drops script and style bodies and `data:` values
and caps nesting at 256 by modelling the parser's stack of open elements
(ruling R24): end tags follow the tree-construction rules by element
category (a special end tag closes its element only within scope, a
formatting one only when it is on top, any other walks down and stops at
a special element), with namespaces for SVG and MathML, the implied
table body, row and p closings, and formatting elements reopened after
another tag closed them; where the rules are more involved the model
closes less, so it errs towards depth. A differential property test
(`tests/depth.rs`) parses the pre-pass's output of random tag soups
from a fixed seed with html5ever and asserts the tree is never more than
12 deeper than the cap (the deepest seen: 8 over); after parsing, any
element deeper than 320 is replaced by its text, so recursion stays
bounded whatever the model missed; `dom_query` (html5ever) parses; an own
Readability-style extractor (iterative walks, table rows scored as
paragraphs, data tables never pruned, link-heavy furniture pruned)
picks the content; links are made absolute and only http(s) and mailto
kept; `htmd` writes Markdown, with table padding removed.

#### B. Rust: `dom_smoothie` (a Readability.js port) and `htmd`

The design's Rust option as written.

#### C. TinyGo: own extractor and renderer on `golang.org/x/net/html`

The design's TinyGo option as written; the extractor and renderer are
the same algorithm as A, in Go.

#### D. TinyGo: `go-readability` and `html-to-markdown`

Ready-made Go libraries compiled with TinyGo.

### Search backends

- **Brave Search API** with `BRAVE_API_KEY`: official, documented, JSON.
- **DuckDuckGo HTML** (`html.duckduckgo.com/html/`): no key, an
  unofficial page.
- **SearXNG**, self-hosted: no key, but the owner runs another service.
- **None** without a key.

## Comparison

The spike, on the same machine (amd64, load average about 2), wazero's
compiler, `modbench -n 20…100`, one process per variant. Pages saved on
2026-11-03: a BBC news article (277 KiB), Effective Go (140 KiB), a
Hacker News thread (98 KiB), the NBU's exchange-rate page (159 KiB), a
generated hostile page (2,108 KiB: 50 scripts, a 2 MB base64 `data:`
image, 5,000 nested `div`s, unclosed tags, an injection sentence) and,
as a stress case beyond the criteria, two Wikipedia lists (850 KiB and
1,353 KiB). p95 of a call in ms:

| Variant | `.wasm` | news | docs | forum | NBU table | hostile | Wikipedia 1.35 MB | Quality |
|---|---|---|---|---|---|---|---|---|
| **A** final (opt 3, memchr, WASM SIMD) | **1.20 MB** | **59** | **80** | **76** | **66** | **72** | 417 | all five right; the NBU rates table kept |
| B (`opt-level = "z"`) | 1.31 MB | 72 | 146 | 237 | — | **timeout (10 s)** | 450 | good articles |
| B (opt 3, A's pre-pass and nesting cap) | 1.62 MB | 83 | 116 | 149–167 | 138–158 | 184 | 390 | took the NBU menu, dropped the rates table |
| C (default GC and scheduler) | 2.00 MB | 241 | 173 | 164 | — | 773 | 2,002 | as A |
| C (`-gc=leaking -scheduler=none`) | 0.93 MB | 172 | 119 | 100 | — | **trap** | 826 | as A |
| D | 3.94 MB | 246 | 347 | — | — | — | — | not pursued: over 3 MB |

Notes:
- B stalls on deep nesting: natively `dom_smoothie` took 0.4 s at 500
  levels, 2.4 s at 1,000 and 17.7 s at 2,000 (cubic); A's pre-pass and
  iterative walks make depth cheap.
- B is a faithful Readability port and inherits its blind spot: a page
  whose content is a table of short cells (the NBU rates) scores nothing,
  so it returns the navigation. A scores table rows and never prunes a
  data table.
- C is slow in WASM, not in its algorithm: natively the same Go code
  converts the 1.35 MB page in about 90 ms. Decoding a 2 MB payload with
  `encoding/json` alone took 228 ms p50 in TinyGo's WASM, and
  `x/net/html` parsing it another 170 ms. Without the scheduler the
  parser's depth limit (a `panic` it recovers from) traps, because
  TinyGo cannot recover there.
- D took 9 min 20 s to link and is over the size limit.
- In A, scanning bytes with a plain loop cost about ten times more in
  WASM than natively; `memchr` with WASM SIMD (which wazero supports)
  took the hostile page from 150 ms to 72 ms p95.
- The fixed cost of moving a 2 MB page into a module and back (a module
  that only decodes the input) is 20–40 ms.
- The four small fixtures committed in `modules/html2md/testdata`
  (2–4 KiB each, written for the tests) take 8–9 ms p95.

Memory of A (principle 8), process RSS in MiB: 11 before loading, 44–47
after compiling the module (once, about 300 ms), 21–25 after 100 calls,
peak 47–74 on the real pages. The first limits (2 MiB of HTML, 64 MiB of
module memory) let a 1.35 MB page peak at 108, and 1.8 MB of stray end
tags at 143 after a 5 s timeout; a synthetic 2 MiB of dense markup took
1.8 s at 128 MiB and peaked at 391. wazero grows a module's memory by
copying a Go slice, and the old copy waits for the garbage collector,
which roughly doubles a call's peak.

The limits now (review fix, ruling R20): HTML at most 1 MiB (JSON and
text keep 2 MiB), module memory 32 MiB, and the runner backs each
instance's memory with one anonymous mapping of its maximum size, so
growing never copies and closing the instance returns the pages at once.
Worst cases at these limits, `modbench`, process maxrss with a 12 MiB
baseline: 1 MiB of dense markup (30,000 table rows and paragraphs) and
1 MiB of nested `div`/`span`/`a` both run out of the 32 MiB and fail
cleanly ("could not be converted") at **79 MiB** at most; the first
1 MiB of the Wikipedia list converts at 69 MiB (p95 326 ms);
`<div></q>` and `<div/>` soup at 51 and 69 MiB (117 and 113 ms); the
hostile page at 54 MiB. With the category rules, 1 MiB of
`<span><div></span>`, `<span><p><div></span>`, `<q><div></q>`,
`<label><div></label>`, `<b><div></b>` and `<div><marquee></div>`
converts in p95 132, 145, 132, 133, 122 and 140 ms, at 50 to 67 MiB
(earlier versions of the pre-pass timed out or trapped on them). The HTML bound applies to the text after charset decoding,
which is what html2md gets (a windows-1251 page doubles in UTF-8). So the worst case adds about 67 MiB to a node
whose agent runs at about 40 MB: about 107 MB, under 128 MB. The real
pages keep their numbers (p95 news 29 ms, docs 67, forum 55, NBU 28).

Search, measured from the development machine on 2026-11-03: four
DuckDuckGo HTML requests in a row returned results three times and a bot
check (`202`, an "anomaly" page) once.

| Criterion | Brave | DuckDuckGo HTML | SearXNG | None |
|---|---|---|---|---|
| Needs a key | yes | no | no | — |
| Reliability | an API with a contract | unofficial; bot checks under bursts | as reliable as the owner's server | — |
| Owner effort | an account and a key | none | a server to run | none |
| Meets R-WEB-3's "default needs no key" | no | yes | only with a server | no |

## Decision

**A: `html2md` in Rust**, with an own extractor on `dom_query` and
`htmd` for Markdown. It is the only variant that meets every criterion
on all five pages (1.20 MB, p95 ≤ 80 ms, no trap) and the only one that
keeps the NBU rates table. The algorithm is ours, so a page type that
reads badly is fixed in our code, not in a fork.

The module is built with Rust 1.99.1 (`rust-toolchain.toml`) and these
crates, all at least two weeks old when chosen: `dom_query` 0.28.0,
`htmd` 0.5.6, `memchr` 2.8.3, `serde` 1.0.229, `serde_json` 1.0.154;
`Cargo.lock` pins the rest, with `log` held at 0.4.34 and `smallvec` at
1.16.3 because their latest releases were younger than two weeks. These
are the module's dependencies, not the core's.

**Search: Brave when `BRAVE_API_KEY` is set, DuckDuckGo HTML
otherwise**, and `GANGLION_SEARCH=off` removes `web.search`. DuckDuckGo
keeps R-WEB-3's key-free default; its bot check is detected and
reported as a fixed error that names the remedy (set the key). Its page
is read with linear regular expressions over at most 1 MiB, not with an
HTML parser in the core. SearXNG can be a third backend if the owner
runs one.

**The fetch** (package `web`):
- the address fetched is `urlnorm.Normalize`'s output, parsed by
  `net/url`: the string the policy checked;
- a dialer resolves the name itself, refuses the host if any address is
  not public (loopback, private, unique-local `fc00::/7`, link-local with
  `169.254.169.254`, CGNAT `100.64.0.0/10`, documentation ranges,
  local-use NAT64 `64:ff9b:1::/48`, unspecified, multicast,
  reserved; IPv4-mapped, NAT64 and 6to4 addresses judged by their IPv4
  address), and connects only to the checked addresses, with a socket
  control hook checking once more; so DNS rebinding and redirects are
  covered;
- no proxy from the environment, no cookie jar, no credentials in an
  address, no `Referer` on redirects;
- redirects (at most 5) are checked like the first address and, in a
  scheduled task, must stay on the grant's hosts;
- a host written as one number or in short or hex form (`2130706433`,
  `127.1`, `0x7f000001`) is read as the IPv4 address it is and checked;
  each address gets its own connect timeout (5 s);
- 2 MiB body (1 MiB for HTML), 15 s, 20,000 characters returned;
  HTML through `html2md`, JSON and plain text as they are, any other
  type an error naming it; charsets such as windows-1251 decoded to
  UTF-8;
- every error is a fixed text (status, type, limit), so remote words
  reach the model only inside the `<external>` wrapper;
- the arguments of every tool call must be exactly the tool's declared
  parameters (`tool.CheckArgs`, before the policy; ruling R18), so a
  case-variant `URL` or a second `url` cannot carry an address past the
  URL rule; and search results make known only each hit's own address,
  written so the agent reads it back exactly, with every other address
  in titles and snippets defanged (`internal/urlnorm`, ruling R19);
- `html2md` runs as a tool module without the `log` host function, so
  nothing it reads reaches the node's log.

The core's `go.mod` gets no new module: `golang.org/x/net` (for
`html/charset`) and `golang.org/x/text` move from indirect to direct.

## Consequences

- Updating the HTML parser or the extractor is a new `html2md.wasm`;
  M3c loads one from the data volume ahead of the embedded one.
- The `.wasm` is committed (1.2 MB) and embedded, so building the core
  needs no Rust. Rebuilding it needs rustup; the build is reproducible
  (paths remapped, toolchain pinned), and CI rebuilds it, runs its tests
  and the Go fixture tests against the rebuilt module, and fails if the
  committed file differs from its sources.
- The module is compiled on the first HTML page, not at start
  (`runner.LazyTool`): the hourly rates task reads JSON and never needs
  it. Measured with `make memory` against the commit before this ADR:
  the binary grows from 35.3 MB to 41.5 MB (the 1.2 MB module, wazero now
  linked into the core, the charset tables); the agent's RSS after 1,000
  turns from 37.8 MB to 39.9 MB. The first page costs about 0.3 s of
  compiling and 10–14 MiB of RSS that stays; each page then costs tens
  of ms and a transient peak of at most about 67 MiB.
- HTML over 1 MiB is refused, and pages near that size of dense markup
  take 0.3 s or fail to convert rather than take the node's memory.
- The "module as a tool" path (`runner.LoadTool`, `Call`) exists before
  M4; a tool module's manifest that grants any capability is refused.
- Without a key, search can fail with a bot check; the owner sees why.
- The extractor is ours to maintain: a site that reads badly becomes a
  fixture and a fix.

## Revisit when

- Pages the owner reads come out wrong often enough that a maintained
  Readability port is worth its cost, or `dom_smoothie` gains table
  handling and loses its nesting cost.
- DuckDuckGo's bot checks show up in the journal more than now and then:
  make search off without a key, or add SearXNG.
- A Pi's measurements disagree with the numbers here (they come from an
  amd64 machine).
