# ADR-0002: Keep docs as Markdown and Mermaid in the repo, readable in Obsidian

- **Status:** Accepted
- **Date:** 2026-11-01
- **Deciders:** Igor Artasevych
- **Spec:** —

## Context

The project needs a way to regain the big picture quickly after switching
from other projects: what the system consists of, how the parts talk, where
each thing lives in the code, and what is done. That calls for overview
pages and diagrams at several levels of detail, linked to each other, the
spec and the ADRs.

Constraints:

- the repository will become public; docs must render on GitHub;
- coding agents read the repository, so docs must be plain text in it;
- diagrams must stay editable as text and diffable, so they are updated
  together with the code instead of rotting in a drawing tool;
- the owner would like a navigable "knowledge base" view (graph, backlinks).

## Options

### A. Markdown + Mermaid in `docs/`, Obsidian as an optional reader

Plain Markdown with standard relative links; diagrams as Mermaid code
blocks. GitHub renders both. Opening `docs/` as an Obsidian vault adds graph
view, backlinks and quick switching, with no Obsidian-only syntax in files.

### B. An Obsidian-first vault

Wikilinks (`[[note]]`), Canvas, Excalidraw and other plugins.

### C. Structurizr DSL (C4 model as code)

One model file generates all C4 views consistently.

### D. An external docs tool (Claude Docs, Notion)

## Comparison

| Criterion | A. MD + Mermaid | B. Obsidian-first | C. Structurizr | D. External |
|---|---|---|---|---|
| Renders on GitHub | Yes | Partly: wikilinks and canvases do not | Needs a rendering step or images | No |
| Text-diffable diagrams | Yes | Excalidraw/Canvas are JSON blobs | Yes | No |
| Agents can read and update | Yes | Mostly | Yes, but views need a tool | Only with a connector |
| Graph and backlinks | In Obsidian | Yes | No | Partly |
| Extra tooling | None | Obsidian + plugins | Structurizr CLI or server | Account |
| One model behind all views | No, views kept consistent by hand | No | Yes | No |

## Decision

**A.** It satisfies every hard constraint with zero tooling, and Obsidian
still gives the navigation the owner wants. Structurizr's single model is
the main thing we give up; at this size, keeping five diagram pages
consistent by hand is cheaper than another toolchain.

Rules:

- standard Markdown links with relative paths, never `[[wikilinks]]`;
- only Mermaid diagram types GitHub renders: `flowchart`, `sequenceDiagram`,
  `stateDiagram-v2`; planned parts are drawn dashed;
- architecture pages follow C4 levels: context → containers → components →
  runtime flows → code map.

## Consequences

- In Obsidian: Settings → Files and links → turn off "Use [[Wikilinks]]" and
  set "New link format" to "Relative path to file", so new links stay
  GitHub-compatible. `docs/.obsidian/` is git-ignored.
- Diagrams are updated in the same commit as the code that changes them.
- No generated consistency between views; the code map is the cross-check.

## Revisit when

Diagrams drift out of sync more than once per phase, or the container
count grows past what one page can show — then reconsider Structurizr.
