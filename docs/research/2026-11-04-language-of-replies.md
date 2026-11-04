# The language of the agent's replies

- **Date:** 2026-11-04
- **Question:** the owner saw a reply in English after web results, though they
  wrote in Russian. In which contexts does the agent lose the language, and is
  the rule or the model at fault?
- **Status:** measured with chatlab
  ([ADR-0048](../adr/0048-chatlab-over-the-fake-bot-api.md)); fixed for scheduled
  tasks.

## Setup

Eight scenarios under `tools/chatlab/scenarios/language-*`, in Russian and in
Ukrainian, 5 repeats each. A judge model reads only what the model wrote, as fixed
texts stay in English (ruling R46).

| Context | What happens |
|---|---|
| first | One message in the language |
| after-tools | The owner asks about an English page, and the model reads it |
| after-confirmation | A reminder is approved by a press; the reply follows English fixed texts |
| task | An hourly task reads a page in another language: Russian owner, Ukrainian page; Ukrainian owner, English page |

The persona is the example one, which says "Use the language you are addressed in".

## Baseline

| Context | Sonnet | Haiku |
|---|---|---|
| ru first, after-tools, after-confirmation | 5 of 5 each | 5 of 5 each |
| uk first, after-tools, after-confirmation | 5 of 5 each | 5 of 5 each |
| ru task | **2 of 5** | 4 of 5 |
| uk task | 4 of 5 | 5 of 5 |
| Total | 36 of 40 | 39 of 40 |
| Tokens | 622,643 | 618,595 |

The problem is in scheduled tasks, on both models, so the rule is at fault and not
the model. The difference between 36 and 39 of 40 is too small a sample to favour
Haiku.

The reply in English after web results that the owner saw did not appear in 10
repeats on each model. That run was on the owner's earlier catalogue, and a
different page. This note does not explain it.

## Cause

A task's run has no words of the owner's: only the instruction the model wrote
when it created the job, in any language, and the page it reads.

| The instruction | Sonnet's replies in the baseline |
|---|---|
| Names the language ("in Russian", "українською") | 6 of 6 right |
| Does not name it | 0 of 4: one in English, two in the page's Ukrainian, one with no words |

So the language came from what the run could read, and the instruction often did
not say.

## Fix

1. `schedule.create`'s description: write a task's text in the language the person
   is using, and say in it which language the answer is to be in.
2. The line a task's run gets: write the answer in the language of the instruction.

After this, every instruction in the 10 repeats was in the owner's language and
named the answer's language.

| Run | ru task | uk task | Note |
|---|---|---|---|
| Fix 1 and 2, the full set of 8 (635,958 tokens) | 4 of 5 | 5 of 5 | The other six stayed at 5 of 5. The one miss was in Russian with a phrase quoted from the page and translated, so the claim was too strict |
| The claim allows a quoted phrase (247,067 tokens) | 4 of 5 | 5 of 5 | A real miss: the reply was in Russian, with "сьогодні" and "листопада" taken from the page |

Ukrainian and Russian are close, and the model kept words of the page. So the line
now also says: translate what you read, and keep no words of another language.

| Run | ru task | Note |
|---|---|---|
| The line also asks to translate (263,765 tokens) | **10 of 10** | Twice the repeats, as 5 of 5 says little about a miss in one of ten |

## What this does not show

- **A rate.** 10 of 10 does not rule out a miss in one of twenty.
- **Other pairs of languages,** or a page in a language close to the owner's other
  than this one.
- **The change of claim.** The task scenarios' claim changed between the runs: it
  no longer counts numbers, currency codes, symbols or a quoted phrase. That
  explains one miss above, and a reply of bare numbers in the baseline; it is not
  why the rest got better.
