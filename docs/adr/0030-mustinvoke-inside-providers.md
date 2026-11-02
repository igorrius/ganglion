# ADR-0030: Take dependencies with do.MustInvoke inside providers

- **Status:** Accepted
- **Date:** 2026-11-02
- **Deciders:** Igor Artasevych
- **Spec:** [2, principle 5](../spec.md#2-principles); supersedes the rule on `Must*` in [ADR-0028](0028-samber-do-composition-root-in-config.md)

## Context

ADR-0028 says: "Providers return errors; `Must*` calls are for tests."
Following it, every provider in `config` checked an error after each
dependency. To keep that short, the providers used a helper, `need[T]`,
that remembered the first error.

The owner found the helpers hard to read. A provider is meant to be the
plainest code in the repository: take the dependencies, call the
constructor.

What samber/do v2.1.0 does with a panic:
- a lazy or transient provider runs through `handleProviderPanic`
  (`invoke.go`);
- that function recovers a panic and returns it as the error of the
  `Invoke` that built the service;
- `MustInvoke` panics with the error value itself, so `errors.Is` and the
  message survive.

## Options

### A. Return errors, with a helper (ADR-0028 as written)

`need[T](i, &err)` for each dependency, one check at the end. It is
correct, but it is one more idiom to learn, and each provider carries
`var err error` and the check.

### B. `do.MustInvoke` inside providers

A provider takes each dependency with `do.MustInvoke[T](i)`. A dependency
that fails panics. `do` recovers the panic, and it becomes the error of
the outer `Invoke`. A provider returns only its own constructor's error.

### C. `do.InvokeStruct` with tagged fields

One struct per provider, its fields tagged `do:""`, filled by reflection.
That means one type per provider, and a dependency is visible only in the
struct's tags.

## Comparison

| Criterion | A. need | B. MustInvoke | C. InvokeStruct |
|---|---|---|---|
| A provider reads as "dependencies, then constructor" | With noise | Yes | Split between a struct and a function |
| Errors at start keep their text | Yes | Yes (do recovers the panic as an error) | Yes |
| Own helpers | `need` | None | None |
| The documented samber/do style | No | Yes | Yes |

## Decision

**B.** The owner's words (2026-11-02): it reads simply, and panicking at
start is even wanted. The rules:

- **Inside providers:** `do.MustInvoke` / `do.MustInvokeNamed` for
  dependencies. A provider returns its constructor's error.
- **Outside providers:** `do.Invoke` with an error, for example
  `config.Run` building the components. No provider runs there, so
  nothing would recover a panic.
- **Extension points:** one helper, `choose`. Each implementation is
  registered under an explicit name constant, with `do.LazyNamed` for a
  provider or `do.EagerNamed` for a ready value such as a no-op. `do`
  names are shared by all types, so each constant is unique.

## Consequences

- `need`, `impl`, `implKey` and `value` are gone from `config`.
- A programming error inside a provider, such as a nil pointer, becomes a
  start error instead of a crash with a stack trace. At start that is
  acceptable, and the message names the provider's service.
- The tests that build every role's container (`config`) keep catching a
  missing provider.

## Revisit when

- samber/do stops recovering panics in providers, or recovers them
  differently.
- Providers start running outside the container's control (eager
  construction at registration).
