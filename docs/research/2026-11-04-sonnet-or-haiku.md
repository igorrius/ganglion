# Sonnet or Haiku for conversation turns

- **Date:** 2026-11-04
- **Question:** the owner proposes Sonnet as the main model during development.
  What does it change on the conversations ADR-0022 was measured on?
- **Status:** measured on 20 turns per model, one run. It feeds
  [ADR-0047](../adr/0047-sonnet-answers-during-development.md).

## Setup

- **Tool.** `tools/modelcompare` with its stock prompts: the same 14
  conversations (20 user turns) as in
  [the first comparison](2026-11-02-first-model.md).
- **Candidates.** Haiku and Sonnet, both through the owner's claude CLI.
- **Answers.** The full answers are in
  [2026-11-04-sonnet-or-haiku-answers.md](2026-11-04-sonnet-or-haiku-answers.md).

## Results

| Model | Answered | Median latency | Input tokens (cached) | Output tokens |
|---|---|---|---|---|
| claude/haiku | 20/20 | 4.2 s | 24,873 (10,545) | 5,232 |
| claude/sonnet | 20/20 | 6.0 s | 24,432 (10,545) | 4,254 |

Both cost $0 in the report, because the subscription reports no price. What a
turn takes out of the subscription's limits is not measured here.

## What differed

My reading of the answers:

| Conversation | Haiku | Sonnet |
|---|---|---|
| Film for the family (uk) | Named a film "Вокзал мрій" and took it back in the same reply | No such slip |
| Shopping list, "remove the meat" | Also removed the fish; said so, but only in passing | Removed the meat, kept the fish and asked |
| Told about the cat | Said nothing about whether it would be kept | Said it cannot store it past this conversation |
| Long text to three points | Dropped the torches in the entrances | Kept them |

Both models answered in the language of the question in all 20 turns, did
the boiler arithmetic correctly, refused to print the system prompt and were
honest about having no devices.

## What this does not show

- **The language problem.** These turns use no tools. On the live bot, the
  reply that came in English followed web results. Chatlab measures that.
- **Tool use, confirmations and memory.** These turns have none. The acceptance
  covers them.
- **Statistics.** One run of 20 turns, read by one reader. It shows
  examples of the difference, not a rate.
