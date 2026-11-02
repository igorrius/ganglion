# ADR-0023: Publish JSON Schemas for ganglion's YAML files, kept in sync with the Go loaders by tests

- **Status:** Accepted
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [7.3](../spec.md#73-model-router), [7.5](../spec.md#75-runner-and-wasm-modules); [ADR-0012](0012-yaml-manifests.md)

## Context

ganglion's YAML files are read by Go loaders that reject unknown fields:
- the model catalogue (`router/rules.LoadCatalog`, M3a);
- module manifests (`runner.ParseManifest`, ADR-0012).

The node config (D-3, M4) and the skills' frontmatter will follow. Today
the only description of these files is the loader code and the commented
examples.

Who reads these files:
- **People editing them.** An editor with a schema completes keys and
  marks mistakes as you type (yaml-language-server, VS Code, GoLand).
- **The agent.** In M6 it proposes configuration changes, and in P3 it
  writes manifests. MCP describes a tool's input as JSON Schema, so a
  config tool can hand the schema to the model as its `inputSchema`.
- **Future operators,** once the repository is public.

## Options

### A. Hand-written schemas, checked against the loaders by tests

A `*.schema.json` per file, with descriptions and enums written for
people. Tests compare each object's properties with the YAML tags of the
loader's structs, and each enum with the Go constants. No new dependency.

### B. Schemas generated from the Go structs

A generator (`invopop/jsonschema`, or about 200 lines of our own) reflects
the structs, and a test checks that the committed file is fresh. There is
one source of truth, but it adds a dependency or a generator to maintain.
Descriptions, enums and conditional rules move into struct tags.

### C. No schemas yet

The commented examples and the strict loaders are the documentation. The
question comes back with the node config in M4.

## Comparison

| Criterion | A. Hand-written + tests | B. Generated | C. None |
|---|---|---|---|
| Editor help and agent input schema | Yes | Yes | No |
| Drift from the loader | Caught by tests (fields, enums) | Impossible for fields | — |
| Rich descriptions, conditional rules (`if`/`then`, `oneOf`) | Natural | Awkward in tags | — |
| Dependencies | None | One, or own generator | None |
| Effort now | Two small files and two tests | Generator plus tags | None |

## Decision

**A.**
- **Location.** The schemas live in `schemas/` and are embedded by the
  `schemas` package, so the agent's tools can serve them at run time.
- **Who validates.** The Go loaders stay the validators at start; the
  schema never replaces them.
- **Tests.** Each loader package has a test: schema properties equal YAML
  fields, `additionalProperties: false` wherever the loader is strict,
  enums equal the constants, and the shipped example files load.
- **Editors.** Example files point at their schema with a
  `# yaml-language-server: $schema=…` line.

## Consequences

- `schemas/models.schema.json` and `schemas/module-manifest.schema.json`
  exist now.
- The node config of M4 (D-3) ships with its schema and the same test.
- The tests catch a renamed or added field and a changed enum. They do not
  catch rules that exist only in Go code, such as "a trigger sets exactly
  one of cron and tool". Such rules are mirrored in the schema by hand
  (`oneOf`, `if`/`then`) and reviewed with the loader.

## Revisit when

- The schemas grow past what is pleasant to maintain by hand.
- The rules that exist only in Go drift from the schema in practice.
