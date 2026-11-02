# First model for conversation turns (D-6)

- **Date:** 2026-11-02
- **Task:** M3a task 10 ([plan](../plans/p1-m3a-talking-agent.md)), decision D-6
- **Question:** which model should answer the agent's conversation turns
  first, and which should take over when it fails?
- **Status:** measured on 20 turns per model. The owner picks; the choice
  becomes ADR-0022.

## Setup

- **Tool.** `cmd/modelcompare` (`make compare`): the same 14 conversations
  (20 user turns) go through each model. History is kept per conversation,
  and the agent's default system prompt is used.
- **Prompts.** Russian, Ukrainian and English:
  - small talk;
  - a follow-up that needs the history;
  - boiler arithmetic;
  - a list edit;
  - a capability-honesty check;
  - a prompt injection;
  - a translation;
  - the date;
  - a long text to shorten;
  - a film recommendation.
- **Candidates.** Claude Haiku through the owner's claude CLI (ADR-0021),
  and two cheap tool-capable models on OpenRouter, picked from its model
  list on 2026-11-02:
  - `deepseek/deepseek-v4.1-flash`: $0.045 / $0.50 per million tokens;
  - `qwen/qwen3.8-flash`: $0.15 / $0.47.

  Gemini Flash was left out because it now costs $0.75 / $3.75.
- **Answers.** The full answers are in
  [2026-11-02-first-model-answers.md](2026-11-02-first-model-answers.md).

## Results

| Model | Answered | Median latency | Input tokens (cached) | Output tokens | Cost of 20 turns | Per turn |
|---|---|---|---|---|---|---|
| claude/haiku (CLI, subscription) | 20/20 | 3.5 s | 14,964 (0) | 4,548 | $0 | $0 |
| openrouter/deepseek-v4.1-flash | 20/20 | 2.3 s | 3,638 (0) | 5,700 | $0.003 | $0.00015 |
| openrouter/qwen3.8-flash | 20/20 | 6.1 s (up to 12.9 s) | 3,658 (0) | 5,119 | $0.003 | $0.00015 |

### Haiku through the CLI

**Strengths:**
- Its answers were good and well structured, and its Ukrainian was correct.
- It got the physics right.
- It refused the prompt injection politely.
- It knew today's date: the CLI adds it itself, which the other models do
  not get.

**Weak spots:**
- One of its two jokes made no sense.
- Its film list "for the whole family" included *La La Land*.
- It sends about 550 extra input tokens per call. This costs nothing on
  the subscription, but it counts towards the subscription's limits.

### DeepSeek V4.1 Flash

**Strengths:**
- It was the fastest of the three.
- Its Russian and Ukrainian were natural, and its physics correct.
- It handled the history-dependent follow-ups correctly, and its film
  picks after 2020 were good.
- It was honest that it does not know the date.

**Weak spots:**
- It sometimes tells people, unasked, that it cannot control devices.

At 300 turns a day it would cost about $1.4 a month.

### Qwen 3.8 Flash

It is the weakest of the three:
- It is the slowest.
- It copied the `Igor:` name prefix into its own answers.
- It used a calque, «курячі гудзики» (for chicken nuggets).
- It gave a wrong Ukrainian title for *The Mitchells vs. the Machines*.
- Its bot names were weak.

## Findings

1. **Haiku and DeepSeek are both good enough** for the agent's conversation.
   Qwen is not.
2. **The model does not know the date.** No model has it except through
   the CLI. M3b should add the current time to the last user message, as
   the plan's stable-prefix rule already says.
3. **Prompt caching did not show on these short prompts.** Each
   conversation has at most two turns, and the system prompt is shorter
   than the providers' minimum cacheable length.
4. **Tools need DeepSeek for now.** Claude CLI models have no tools until
   M3b (ADR-0021), so DeepSeek is also the model for tool-using tasks
   until then.

## Recommendation

- **Haiku through the CLI first.** It is free inside the subscription and
  its quality is good.
- **DeepSeek V4.1 Flash as the fallback,** and as the model for tasks that
  need tools.
- **Both in the `cheap` tier.** The router then tries Haiku first (price
  0), then DeepSeek, for both messages and compaction.
- **Sonnet stays out of the catalogue** until a task shows Haiku is not
  enough.
