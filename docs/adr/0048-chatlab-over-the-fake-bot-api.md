# ADR-0048: Test the agent by talking to it, through a separate program over the fake Bot API

- **Status:** Proposed
- **Date:** 2026-11-04
- **Deciders:** Igor Artasevych
- **Spec:** [2](../spec.md#2-principles) (principle 9), [9.8](../spec.md#98-self-modification-levels); [chatlab design](../plans/p1-m3c-chatlab-design.md); [plan](../plans/p1-m3c-chatlab.md)

## Context

The live checks on the test bot do not work as a method. The owner cannot always
tell what to do, what to expect or how long to wait, and one scripted check
tested nothing: the model found its addresses through search, so the question
under test could not appear. The list grows with every milestone.

`make accept` is half of the answer: a real core, real models and `telegramtest`
playing the owner. Its scenarios are Go code that knows Telegram, nobody can hold
a conversation with it, and it cannot judge what a reply means.

The owner wants the developer to run these checks, read the results and fix what
they find, through abstractions of our own, with no real Telegram.

## Options

### A. A connector over the fake Bot API

A channel-neutral `Peer`, connected to the real Telegram adapter through
`telegramtest`. The adapter's rendering, splitting and buttons run as in
production.

### B. A loopback transport that skips the adapter

A second `transport.Transport` that hands messages over directly. Simpler, but
the adapter stays untested, and the transport itself is new code that only tests
use. A bug in how a question is split or edited would pass.

### C. A real Telegram client

An MTProto user account driving the real bot. It is the closest to a person, and
it needs a phone number, is slow and flaky, and can touch a real account. The
owner ruled it out.

Separately, where the code lives:

- **In the `ganglion` binary, behind a flag.** One program, but the clocks and
  fakes would ship in it.
- **In a separate program behind a build tag.** The overrides stay out of the
  binary, and a test holds that.

## Comparison

| Criterion | A. Fake Bot API | B. Loopback | C. Real client |
|---|---|---|---|
| Real adapter runs | Yes | No | Yes |
| Real Telegram's behaviour | No: a short look by the owner | No | Yes |
| Can touch a real bot or account | No | No | Yes |
| Time can be moved | Yes | Yes | No |
| New production-like code | None | One transport | One client |
| A second channel later | One more connector | One more connector | One more client |

## Decision

**A, in a separate program.** `tools/chatlab` is built with the tag `lab`; library
code is in `internal/chatlab`; `config/lab.go` holds the overrides, those of
`make accept` plus one: the agent reads the scheduler's moved clock, or a model
would see a reminder fire early. Scenarios are YAML. Facts from the journal and files decide
firmly, and a judge model decides what a reply means, counted over repeats.

## Security

- **Trust boundaries:** the lab holds provider keys and runs real models and real
  web fetches, as `make accept` does. New: a local control socket for a session.
- **What an attacker controls:** a web page in a fetch, so a model's output, so
  the text the judge reads.
- **Abuse cases:**
  - A page tells the judge to pass: the reply reaches it wrapped as data, and
    facts outrank it, so at worst a meaning check passes wrongly. A result is
    advice to the developer and takes no action.
  - A lab override ships: `TestLabStaysOutOfTheBinary` fails if the root package
    reaches a fake, `testing` or the lab, or if a default build holds `lab.go`.
  - Another local user drives a session: the socket is `0600` in a `0700`
    directory with an unpredictable name.
  - A scenario points at a real bot or the live database: no such setting exists.
    The connector starts the fake, paths are temporary, a Bot API URL that is not
    loopback is refused, and the lab drops `TELEGRAM_*` from its environment.
  - A key reaches a report: reports are redacted before writing, and a test seeds
    a key through a reply.
  - A runaway scenario spends money: `--max-tokens`, and the node's daily limits.

## Consequences

- `telegramtest` and `providertest` take a small interface instead of `testing.TB`
  and no longer import `testing`.
- How a message looks on a phone stays a short look by the owner.
- Two test frameworks exist for now: `make accept` keeps running until chatlab
  covers it.
- Real-model runs take from the owner's subscription; they run by hand, never in
  CI. Scripted scenarios spend nothing and do run in CI.
- What repeats and needs no judgement needs no model here either, as in the rest of
  the project: mechanics run on a scripted model, the language of a reply is
  checked by its letters, and a judge stays for what a reply means.
- `make lab` runs only what changed since it last passed. A full run, about 900,000
  tokens, is decided and never automatic, not even before a merge (owner,
  2026-11-04); a change to what every reply depends on recommends it.
- A scenario's `covers` is written by hand and can miss a file, so a green `make lab`
  says less than a full run; the table says how many were left out.

## Revisit when

- Chatlab covers `make accept`: move its scenarios and delete one framework.
- A second channel is added: its connector shows whether `Peer` needed Telegram.
