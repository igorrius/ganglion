# ADR-0085: The site publishes the user guide, not the engineering record

- **Status:** Accepted
- **Date:** 2026-11-14
- **Deciders:** Igor Artasevych
- **Spec:** [ADR-0082](0082-open-the-repository-now-and-keep-the-announced-release-in-p3.md),
  supersedes [ADR-0084](0084-publish-docs-with-zensical-on-github-pages.md)
  in what the site publishes; its choice of Zensical stands

## Context

ADR-0084 published all of `docs/` as the site. Once it was live, the owner
looked at it as an outside reader would and found the engineering record:
85 ADRs, 38 plans, research notes and the spec, with nothing that says how
to install, configure or use a node. That is the wrong front door for
people who want to run ganglion, and it buries the few pages they need.

The engineering record is written for the people building the project and
reads well on GitHub, where it lives beside the code. A user needs a
different set of pages: requirements, install, configure, first
conversation, commands, modules, operations, troubleshooting
([#177](https://github.com/igorrius/ganglion/issues/177)).

## Options

### A. Publish everything, add a guide section on top

Keep `docs/` as the site and put the guide first in the navigation.

### B. Publish only a user guide in its own folder

The site is built from `docs/guide/`; the engineering record stays in
`docs/` and is read on GitHub. One page of the guide, "For contributors",
links to it.

### C. A separate repository or branch for the site

## Comparison

| Criterion | A. Everything | B. Guide only | C. Separate repository |
|---|---|---|---|
| What a reader meets | the guide, then hundreds of internal pages in navigation and search | only what a user needs | only what a user needs |
| Search results | ADRs and plans crowd out guide pages | guide pages only | guide pages only |
| Guide and code change together | yes | yes, same pull request | no: two repositories to keep in step |
| Engineering record | on the site and on GitHub | on GitHub, as before | on GitHub |
| Strict link check | over all of `docs/` | over the guide only | over the guide only |

## Decision

**B.** The site is the user guide, built by Zensical from `docs/guide/`.
The guide changes in the same pull request as the behaviour it describes;
the engineering record keeps its place and readers on GitHub. Links from the
guide to anything outside `docs/guide/` are full GitHub links.

## Security

No new boundary: the site is still static pages built from the
repository's own files. The guide is read by strangers, so the rule that
nothing real about a running node goes into the repository applies to
every example: placeholders only.

## Consequences

- The site has a guide's navigation: Getting started, Using ganglion,
  Running a node, Troubleshooting, For contributors.
- The strict build no longer checks links inside the engineering record;
  they are read on GitHub, which renders them.
- A change to a command, a setting or the install path updates the guide
  in the same pull request; `CLAUDE.md` says so.

## Revisit when

Contributors outside the owner need a browsable, searchable engineering
record; then a second section or a second site can publish it.
