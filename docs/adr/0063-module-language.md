# ADR-0063: Write modules in Rust by default, TinyGo when Go code already exists, standard Go only when neither fits

- **Status:** Accepted
- **Date:** 2026-11-10
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-4, R-RUN-5, R-RUN-9), [9.4](../spec.md#94-skill-compilation-p3), [11](../spec.md#11-open-decisions) ("Language of model-written modules"); [M4 design](../plans/p1-m4-reflexes-design.md) section 6.5; [ADR-0011](0011-module-abi.md), [ADR-0056](0056-small-hosts-as-a-soft-target.md), [ADR-0061](0061-local-modules-built-from-source.md)

## Context

The spec left "Language of model-written modules" open until P3, leaning
towards Rust or TinyGo on the P0 numbers. M4 makes it concrete earlier:
- the builder ([ADR-0061](0061-local-modules-built-from-source.md))
  needs to know which toolchains the image carries;
- the `deye-reader` example exists in Rust and in Go, and one of them
  becomes the example;
- the agent's module-writing skill (spec 9.4) will need a rule to read.

P0 measured the same Deye logic under the own ABI (ADR-0011, amd64,
wazero 1.12):

| Language | `.wasm` | Cold load | Call p50 | RSS after calls |
|---|---|---|---|---|
| Rust | 161 KB | 46–70 ms | 1.8–3.7 ms | 20 MiB |
| TinyGo | 669 KB | 490–513 ms | 7.5–7.8 ms | 25 MiB |
| Go | 3.6 MB | 0.84–0.93 s | 22–24 ms | 41 MiB |

On amd64 and arm64 wazero compiles to native code; on 32-bit ARM and
RISC-V it interprets (R-RUN-4), which widens the gap. Module glue exists
for Rust and for Go and TinyGo (`pdk/guest`); any other language needs its
own (R-RUN-5).

## Options

### A. One language for every module

Rust only. One toolchain, one example, one way to review.

### B. Free choice per module

Whoever writes the module picks the language, among those with glue.

### C. A rule

Rust by default for small system modules (HTTP, JSON, arithmetic). TinyGo
when Go code for the task already exists and TinyGo compiles it. Standard
Go only when TinyGo cannot compile it and a rewrite does not pay.

## Comparison

| Criterion | A. Rust only | B. Free choice | C. The rule |
|---|---|---|---|
| Size and memory on a small host | Best | Up to 20× larger, 20 MiB more per module | Best by default; Go only by exception |
| Reusing existing Go code | Rewrite it | Yes | TinyGo first, Go if it must |
| Toolchains a builder carries | One | Every one with glue | Rust always; TinyGo and Go where used |
| A rule the agent's skill can follow | Yes | No | Yes |
| What the model writes reliably | Unknown until P3 | Unknown | Unknown |

## Decision

**C** (owner, 2026-11-09). Rust gives the smallest, fastest modules, which
small hosts need (ADR-0056); Go code that already exists should not be
rewritten for its own sake, and TinyGo keeps most of the gain.

- The rule goes into spec 9.4, where the agent's module-writing skill will
  read it.
- `deye-reader` is the Rust example; the Go version stays as test data and
  a reference (design section 9).
- Languages without glue (AssemblyScript and others) are outside the rule
  until someone writes their glue (ADR-0011).

## Security

No new boundary. A module's rights come from its manifest and the ABI,
whatever its language; the toolchains as a supply chain are covered by
[ADR-0061](0061-local-modules-built-from-source.md).

## Consequences

- The spec's open decision closes as a rule; the question it was to answer
  at the start of P3, how reliably the model writes and fixes each
  language, is still measured then.
- The M4 image carries Rust with `wasm32-wasip1` only. A TinyGo or Go
  module needs a machine with that toolchain; `Can` reports what is there.
- A standard Go module costs about 20 MiB more host memory and close to a
  second to load, which the rule accepts only by exception.

## Revisit when

- P3 shows the model writes or fixes Rust badly, or one of the others
  markedly better.
- wazero compiles on 32-bit ARM or RISC-V, or TinyGo's output shrinks.
- A module needs a language that has no glue.
