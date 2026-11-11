# ADR-0071: Keep facts in topic files and show every topic whole in the prompt, within a budget

- **Status:** Proposed
- **Date:** 2026-11-11
- **Deciders:** Igor Artasevych
- **Spec:** [7.7](../spec.md#77-memory) (R-MEM-6, R-MEM-7), [7.14](../spec.md#714-node-configuration) (R-CFG-8); [M5 design](../plans/p1-m5-memory-topics-design.md) sections 3 and 4; replaces the file layout of [ADR-0033](0033-memory-in-the-workspace-git-with-the-git-binary.md)

## Context

ADR-0033 stores one fact per file, `memory/<slug>.md`, and a turn sees an
index: the title and slug of each fact, at most 200. The body is read
with `memory.read` when the model decides to.

The M4c live check (2026-11-10) showed the cost. The agent read a
negative `batt_w` as discharge after the owner had corrected it. Two
things failed; this ADR is about the second (the first is
[ADR-0072](0072-who-writes-memory.md)): even a stored fact reaches a turn
as a title, and a title such as "Deye battery power sign" does not carry
the fact. The model has to guess that it should read it.

Constraints:
- One operator, one agent (spec 14). Memory is small: the test bot holds
  one fact; the family bot to migrate holds tens.
- The system prompt must stay stable between writes, so provider caches
  keep hitting.
- The owner reads and edits memory as Markdown, and undoes a change with
  git (R-MEM-3, ADR-0033).
- A fact records where it came from (R-MEM-5).
- No tokenizer in the node; no embeddings in P1 (R-MEM-4).

## Options

### A. One fact per file, an index of titles (today)

As ADR-0033. The model reads a body on demand.

### B. Topic files, every topic whole in the prompt within a budget

A topic is one file with a title, a one-line summary and a list of short
facts. The known-facts block holds every topic whole while the total fits
a budget in characters; over it, the topics changed longest ago fold to
"title — summary" and are read on demand.

### C. Topic files, an index in the prompt, a topic added by keyword match

The prompt holds titles and summaries. Code compares the message with
each topic's keywords and adds the matching topics to the turn.

## Comparison

| Criterion | A. Fact files, title index | B. Topics whole, budget | C. Topics by keyword |
|---|---|---|---|
| The fact is in front of the model without a tool call | No | Yes, until the budget is passed | Only when a keyword matches |
| Fails on "how much is it charging now?" for the inverter topic | Yes, unless the model reads | No | Yes, unless "charging" is a keyword |
| Tokens per turn | Lowest: titles | Up to the budget (about 6,000), cached between writes | Low, but varies per message, so less of the prompt is cached |
| Prompt stable between writes | Yes | Yes | No: it depends on the message |
| Owner reads one file per subject | No: a file per fact | Yes | Yes |
| A correction replaces the old statement | Only by writing the same title again | Yes, by fact id | Yes, by fact id |
| Work | None | The format, the block, folding, the move from A | B's, plus keywords and matching |
| Grows to many facts | Index capped at 200 | Folds by budget; matching or embeddings attach to folded topics later | Yes |

## Decision

**B.** With one owner the memory stays small for a long time, so showing
it whole is affordable and removes the guess that failed. The budget
bounds the cost, and folding is where C or embeddings attach later
without changing the files.

The rules, in package `memory`:
- A topic is `memory/<slug>.md`: YAML frontmatter with `title` and
  `summary`, then a list. A fact is one item: `- [f3] statement <!--
  2026-11-11 turn=<id> tainted -->`. The id is stable inside its topic and
  never reused; the comment holds the date, the turn and the taint. A
  hand-written line with no comment is a fact with no recorded source; a
  line with no id gets one at the next write to its topic.
- Limits on write: a fact 300 runes, a title 100, a summary 150, 50 facts
  in a topic.
- The block: the fixed header, then each topic in slug order as its
  title and its facts with ids and the "(from outside content)" mark.
  `memory.prompt_budget` (characters, default 24,000) bounds it. Over the
  budget the topic changed longest ago folds first, to one line; ties
  break by slug. Folding depends only on the files.
- Git, one commit per change, the quarantine directory and the "no git,
  no memory" rule of ADR-0033 stay. A quarantined fact is stored on its
  own under `memory/quarantine/`, with its topic named.
- At start, files in the old shape become topics with the same slug and
  title, an empty summary and one fact that keeps its text, date, turn
  and taint; one commit for all.

## Security

- **Trust boundaries.** None new: the block is still shown as data, to
  admins only, and every fact passed the clean-context check
  ([ADR-0034](0034-clean-context-guard.md)).
- **What an attacker controls.** Text that reached a fact through a
  tainted session the owner approved, or a line added to a file on disk.
- **Abuse cases.**
  - *A planted instruction is now in every turn, not behind a read.* The
    check runs on every write and nightly on every fact; a fact from a
    tainted session needs the owner's press and carries its mark in the
    block. This is the same exposure the titles had, with more text.
  - *A fact that closes the data block or imitates a topic heading.* A
    fact is one line; the renderer strips line breaks and the block's own
    markers from titles, summaries and facts.
  - *A huge file pushes real facts out of the block.* Limits on write, and
    the budget folds whole topics, never part of one; a file over the
    limits on disk is folded and logged.

## Consequences

- A stored fact is seen in every turn of every later session.
- A write changes the system prompt: one cache miss per write.
- A turn carries up to the budget in memory text; the gauge of R-OBS-8
  shows the block's size.
- The memory tools change shape: `memory.write` takes a topic and a
  fact, `memory.read` a topic, `memory.forget` a fact or a topic.
- A wrong fact is in every turn until it is undone (R-MEM-9).

## Revisit when

- The block is folding topics that turns then needed: add matching or
  embeddings for folded topics (R-MEM-4).
- Memory serves more than one person, each with their own facts.
