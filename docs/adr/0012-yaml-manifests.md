# ADR-0012: Write module manifests in YAML, parsed strictly with go.yaml.in/yaml/v3

- **Status:** Accepted
- **Date:** 2026-11-01
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules), R-RUN-1

## Context

Every module ships a manifest: triggers, capabilities, config, limits,
escalation rules. Manifests are written by people now and by the model in
P3, and they are shown to the admin in the install summary. The manifest is
a security boundary: a capability that is misspelled and silently ignored
would either break the module or, worse, hide what it really asks for.

## Options

### A. YAML with `go.yaml.in/yaml/v3`

The maintained continuation of `gopkg.in/yaml.v3`, from the YAML
organisation. Supports strict decoding (`KnownFields`).

### B. JSON with the standard library

### C. TOML (`BurntSushi/toml` or `pelletier/go-toml`)

## Comparison

| Criterion | A. YAML | B. JSON | C. TOML |
|---|---|---|---|
| Readable in a Telegram install summary | Good | Noisy | Good |
| Comments for people | Yes | No | Yes |
| New dependency | One, small, no transitive deps | None | One |
| Unknown fields rejected | Yes (`KnownFields`) | Yes (`DisallowUnknownFields`) | Yes |
| Consistency with skills (`SKILL.md` frontmatter is YAML) | Same format | Different | Different |
| Pitfalls | Implicit typing (`no`, `0123`) | Few | Few |

## Decision

**A.** Manifests stay readable and commented, match the YAML frontmatter
that skills already use, and strict decoding turns a typo into an error
instead of a dropped restriction. The YAML typing pitfalls are contained by
decoding into typed structs and validating limits and triggers.

## Consequences

- `runner.ParseManifest` rejects unknown fields, a missing name, triggers
  that set neither or both of `cron` and `tool`, and limits it cannot parse.
- The same library can parse `SKILL.md` frontmatter in P1.

## Revisit when

The model regularly produces YAML that trips the parser, or a manifest
needs a schema that YAML expresses poorly.
