# Documentation site generators: what fits docs/ as it is

- **Date:** 2026-11-14
- **Task:** [#178](https://github.com/igorrius/ganglion/issues/178), part of
  [#176](https://github.com/igorrius/ganglion/issues/176)
  ([ADR-0082](../adr/0082-open-the-repository-now-and-keep-the-announced-release-in-p3.md)
  names a documentation site as a next step)
- **Question:** which generator publishes `docs/` on GitHub Pages without
  rewriting it: relative links between Markdown files, about 250 links to
  headings in GitHub's anchor style, Mermaid diagrams, no front matter?
- **Status:** facts gathered from the projects' own pages and source on
  2026-11-14; Zensical and mdBook were built on a copy of `docs/` (148
  files).

## What docs/ needs

| Property of docs/ | Count or example |
|---|---|
| Markdown files | 148 in `docs/`, `adr/`, `architecture/`, `plans/` (since moved to `design/`), `research/` |
| Files with Mermaid | 16 (`flowchart`, `sequenceDiagram`, `stateDiagram-v2`, [ADR-0002](../adr/0002-docs-as-markdown-and-mermaid.md)) |
| Links to a heading | about 250, written as GitHub makes the anchor: "10.1. P0 · Recon — tasks and acceptance" is `#101-p0--recon--tasks-and-acceptance` (punctuation dropped, each space a hyphen, no collapsing) |
| Front matter | none; the title is the first heading |
| Links to files outside `docs/` | about a dozen: `../Dockerfile`, `../ganglion.example.yaml`, `../../SECURITY.md`, … |
| Constraint | the same files stay readable on GitHub and in Obsidian ([ADR-0002](../adr/0002-docs-as-markdown-and-mermaid.md)) |

## Candidates

| | MkDocs + Material | Zensical | Hugo | Starlight (Astro) | Jekyll on Pages | mdBook |
|---|---|---|---|---|---|---|
| Version seen | MkDocs 1.6.1 (2024-08-30), Material 9.8.1 (2026-11-07) | 0.1.1 (Nov 2026), beta | 0.170.0 (2026-11-13) | 0.43.1 | built in | 0.5.4 (2026-07-06) |
| Maintenance | MkDocs core stalled, 2.0 drops plugins and breaks Material; Material in maintenance since 2025-11-05, fixes until 2027-05-05 | active, weekly releases; 0.1 is a three-month beta | active | active, pre-1.0 | stagnant | active (rust-lang) |
| License | BSD-2 / MIT | MIT | Apache-2.0 | MIT | MIT | MPL-2.0 |
| Relative `.md` links | native, validated | native, validated | needs a render-link hook | needs a rehype plugin | plugin, anchors undocumented | native |
| GitHub anchors | with `pymdownx.slugs.slugify` | with `pymdownx.slugs.slugify` (**built and checked**) | default `autoIDType: github` | github-slugger, likely | no: kramdown drops a leading number | native `id_from_content` |
| Front matter | not needed | not needed | title not taken from the first heading | `title` required on every page | required to render | not needed; nav and titles from `SUMMARY.md` |
| Mermaid | superfences fence | superfences fence, JS loaded by the theme | code-block render hook | plugin (client) or build-time with Chromium | custom include | `mdbook-mermaid` preprocessor |
| Navigation | hand or from folders | from folders, or `nav` | section tree | sidebar config | theme | hand-written `SUMMARY.md`, unlisted files not built |
| Search | built in | built in | theme | Pagefind | none | built in |
| CI toolchain | Python | Python ≥ 3.11 | Go binary | Node | none | Rust binary |
| Pages workflow | documented | documented (official) | documented | official action | native | none official |

Sources: pypi.org JSON for mkdocs, mkdocs-material and zensical;
squidfunk.github.io/mkdocs-material/blog/2025/11/05/zensical and
.../2026/02/18/mkdocs-2.0; zensical.org (compatibility, validation,
navigation, diagrams, publish-your-site); gohugo.io (configuration/markup,
render-hooks/links, render-hooks/code-blocks); starlight.astro.build
(authoring-content); kramdown.gettalong.org (converter/html);
rust-lang.github.io/mdBook and `crates/mdbook-html/src/utils.rs`;
pymdown-extensions `pymdownx/slugs.py`.

Hugo, Starlight and Jekyll fail on the files as they are: each needs front
matter added to 148 files or hooks for links and titles, and Jekyll breaks
the numbered anchors. Material is the tool Zensical replaces and has an
announced end. That leaves Zensical and mdBook to build.

## Builds on a copy of docs/

**Zensical 0.1.1** (`pip install zensical`, Python 3.14), a `mkdocs.yml`
with `docs_dir: docs`, `toc` with `pymdownx.slugs.slugify` (`case: lower`)
and the `mermaid` superfences fence:

- 148 pages built in 6 s; the navigation comes from the folders and
  `README.md` becomes each folder's index.
- The `!!python/object/apply` slug setting is honoured: the spec's heading
  gets `id="101-p0--recon--tasks-and-acceptance"`.
- A script followed every link in the built site: **5,304 links to
  headings, none broken.**
- Mermaid blocks come out as `class="mermaid"` and render in the page.
- Four warnings, all links to Markdown files outside `docs/`
  (`SECURITY.md`, `CONTRIBUTING.md`, `CLAUDE.md`, the ADR template's
  placeholder). Links to other files outside `docs/` (`../Dockerfile`)
  pass without a warning and lead nowhere on the site.

**mdBook 0.5.4** with `mdbook-mermaid` 0.17.1 (built with `cargo install`;
CI would download release binaries), `src = "docs"`, and a `SUMMARY.md`
written by a script from the folders (mdBook builds only the files listed
there):

- Built in under a second; the same anchor
  `101-p0--recon--tasks-and-acceptance`, Mermaid rendered.
- **4,995 links to headings, none broken**, except links into a folder's
  `README.md`: mdBook writes `README.md` as `index.html` but leaves links to
  `README.md` pointing at `README.html`, so the 13 such links in `docs/`
  (the old status section among them) lead nowhere.
- 27 warnings for placeholders such as `<name>` and `<title>` written
  outside backticks; see below.
- Links to files outside `docs/` lead nowhere, as with Zensical.

## What follows for any generator

- About ten placeholders in prose are written as bare tags (`<title>`,
  `<reason>`, `<name>`, `<path>`, `<repo>`): GitHub already drops them from
  the rendered page, and in a built site a bare `<title>` swallows the rest
  of the page. They go in backticks. Zensical passes them through silently;
  mdBook warns.
- Links to files outside `docs/` must become full GitHub links
  (`https://github.com/igorrius/ganglion/blob/master/...`); they work on
  GitHub, in Obsidian and on the site alike. There are about a dozen.
- The site lives under `/ganglion/`, so the generator must know its base
  URL.
