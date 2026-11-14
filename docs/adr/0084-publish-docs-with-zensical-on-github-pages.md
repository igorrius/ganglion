# ADR-0084: Publish docs/ as a site with Zensical on GitHub Pages

- **Status:** Accepted
- **Date:** 2026-11-14
- **Deciders:** Igor Artasevych
- **Spec:** [3. Deliberately NOT in 0.x](../spec.md#3-deliberately-not-in-0x),
  [ADR-0082](0082-open-the-repository-now-and-keep-the-announced-release-in-p3.md)

## Context

The repository is public since 2026-11-14, and ADR-0082 names a
documentation site as one of the next steps for outside readers
([#178](https://github.com/igorrius/ganglion/issues/178), under
[#176](https://github.com/igorrius/ganglion/issues/176)). The user guide
([#177](https://github.com/igorrius/ganglion/issues/177)) will be written
into the same site.

`docs/` is 148 Markdown files written for GitHub and Obsidian
([ADR-0002](0002-docs-as-markdown-and-mermaid.md)): relative links between
files, about 250 links to headings in GitHub's anchor style (numbered
headings make anchors such as `#101-p0--recon--tasks-and-acceptance`),
Mermaid diagrams in 16 files, no front matter. The site must publish these
files as they are: a generator that needs front matter on every file or
changes the anchors means rewriting the record, and the files must stay
readable on GitHub and in Obsidian.

The [research note](../research/2026-11-14-docs-site-generators.md) looked
at MkDocs with Material, Zensical, Hugo, Starlight, Jekyll on Pages and
mdBook. Hugo, Starlight and Jekyll fail on the files as they are (front
matter or link and title hooks; Jekyll breaks numbered anchors). Material
is in maintenance mode with fixes until 2027-05-05, and MkDocs 2.0 breaks
it. Two remain.

## Options

### A. Zensical

The generator the Material team builds to replace Material, MIT, Python
≥ 3.11. It reads `mkdocs.yml`, builds the navigation from the folders,
validates links and anchors, renders Mermaid and has built-in search and an
official Pages workflow. Version 0.1 is a beta, with a stable release
expected in about three months.

### B. mdBook

The Rust project's book generator, MPL-2.0, a single binary. It resolves
`.md` links natively, makes GitHub-style anchors and has built-in search.
Every page must be listed in a hand-written `SUMMARY.md`; Mermaid comes
from the `mdbook-mermaid` preprocessor, which copies `mermaid.min.js` into
the repository.

## Comparison

| Criterion | A. Zensical | B. mdBook |
|---|---|---|
| docs/ built as it is | yes: 148 pages, 5,304 heading links, none broken | almost: anchors right, but the 13 links to a folder's `README.md` break (it becomes `index.html`) |
| Navigation | from the folders; nothing to keep in step | `SUMMARY.md` listing every file, or a script that writes it |
| Mermaid | built in | preprocessor plus a vendored `mermaid.min.js` |
| Broken links | warned by default; `--strict` fails the build | not checked without another preprocessor |
| Maturity | beta (0.1), weekly releases | stable (0.5.x) |
| Fallback | the same `mkdocs.yml` builds with Material 9.x, maintained until 2027-05-05 | none needed |
| CI toolchain | Python, `pip install` of a pinned version | a prebuilt binary download, no official Pages action |
| Effort | one config file and one workflow | config, `SUMMARY.md` generation, preprocessor, vendored JS, workflow |

## Decision

**A. Zensical**, pinned to an exact version. It builds `docs/` with no
change to the files and keeps navigation and link checking free of upkeep,
which matters for a record that grows every week. The beta risk is bounded:
the configuration is `mkdocs.yml`, which Material 9.x also builds, so a
breaking Zensical release means switching one line in the workflow, not the
site.

How it is set up:

- `mkdocs.yml` at the repository root, `docs_dir: docs`, `site_url` the
  Pages address; `toc` uses `pymdownx.slugs.slugify` (`case: lower`) so
  anchors match GitHub's.
- The workflow `docs.yml` builds with `zensical build --clean --strict` on
  every pull request that touches `docs/` or `mkdocs.yml` (a broken link
  fails the check) and deploys to GitHub Pages from `master`.
- Links from `docs/` to files outside it are full GitHub links
  (`https://github.com/igorrius/ganglion/blob/master/...`); the
  docs-format rule in `CLAUDE.md` says so.

## Security

The site is static HTML built from the repository's own files; it takes no
input and holds no secret. The workflow gets only the Pages permissions it
needs (`pages: write`, `id-token: write`) and runs on `master` and pull
requests from the repository; third-party actions are pinned by commit
SHA, as in `ci.yml`. What it publishes is what the repository already
shows, so the rule that nothing real about a running node goes into the
repository covers the site too.

## Consequences

- Outside readers get a searchable site with diagrams; the user guide has a
  home.
- A new dependency outside Go: Python and Zensical in CI only, never in the
  node or its image.
- Broken links and anchors in `docs/` now fail a check; about a dozen
  existing links to files outside `docs/` are rewritten first, and about
  ten placeholders written as bare tags (`<title>`, `<name>`) go into
  backticks: GitHub already drops them, and on the site a bare `<title>`
  swallows the rest of the page. A strict build does not catch that; the
  docs-format rule in `CLAUDE.md` says placeholders in prose are always in
  backticks.
- While Zensical is a beta, an upgrade is a deliberate change of the pinned
  version, checked by the strict build.

## Revisit when

Zensical stalls or breaks the strict build on an upgrade without a fix
within a release or two (fall back to Material 9.x while it is maintained),
or the guide needs something the MkDocs model cannot do.
