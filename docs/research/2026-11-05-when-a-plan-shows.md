# When the agent shows a plan

- **Date:** 2026-11-05
- **Question:** on the owner's live try, a request for a report showed no plan and the
  answer rested on search results. Is a clearer rule in the prompt enough, or does telling a
  question from a task need a classifier model?
- **Status:** measured with chatlab, real model and real web; a small sample.

## Setup

Sonnet through the claude CLI. The search is DuckDuckGo, where the owner's node uses another
backend, so what the model finds differs. Three requests:

| Scenario | Request | Wanted |
|---|---|---|
| `task-report` | The owner's own words, typos kept | A plan, and pages opened |
| `task-selection` | A UPS for a gas boiler, three options compared | A plan, and pages opened |
| `question-no-plan` | How many watt-hours in a 20,000 mAh power bank | No plan |

The old rule said "before work with several steps, call `plan.show`". The new one says that a
result you have to put together, such as a report, a comparison or a selection, is work with
several steps, that sources are to be opened rather than search results read, and that a
question that wants a fact gets an answer with no plan.

## Results

| Scenario | Repeats | Old: plan | New: plan | Old: pages opened | New: pages opened | Old: tokens per repeat | New: tokens per repeat |
|---|---|---|---|---|---|---|---|
| `task-report` | 2 | 0 of 2 | 2 of 2 | 2, 2 | 2, 3 | about 32,500 | 64,000 to 78,000 |
| `task-selection` | 1 | 0 of 1 | 1 of 1 | 0 | 4 | 12,600 | 78,900 |
| `question-no-plan` | 2 | 0 of 2 | 0 of 2 | 0 | 0 | about 6,100 | about 6,100 |

The measurement spent about 323,000 tokens. The estimate given to the owner was about
200,000: the new rule cost more per repeat than expected.

## What it shows

- **No classifier for now.** The main model told the three requests apart with the clearer rule.
- **The work costs more.** A report now takes about twice the tokens, and the UPS selection
  six times, since it went from answering from memory to reading pages. Long pages also
  start compaction in the same turn.
- **The display had a fault.** The model ticked only the first and the last step, so lines
  of work it had done read ⏭. A later step that ended now closes the earlier ones.
- **Chatlab had a fault.** It took compaction, on Haiku, for a fallback and called the
  repeats inconclusive. The facts above were read from the reports by hand; the scenarios'
  own verdict comes with their next run.

## What this does not show

- **A rate.** Five requests cannot rule out a plan on some simple question or none on a task.
- **Other wording.** The owner's words and one other request were tried, in Russian only.
- **Whether the report is better.** Pages were opened and sources named; nobody judged the
  content.
