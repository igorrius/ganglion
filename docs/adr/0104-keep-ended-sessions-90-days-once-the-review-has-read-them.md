# ADR-0104: Keep ended sessions 90 days once the memory review has read them, and leave the nightly model pass for later

- **Status:** Accepted
- **Date:** 2026-11-21
- **Deciders:** Igor Artasevych
- **Spec:** [7.7](../spec.md#77-memory) (R-MEM-1, R-MEM-2, R-MEM-8), [7.14](../spec.md#714-node-configuration), [11](../spec.md#11-open-decisions) (how long ended sessions are kept), [14](../spec.md#14-not-now); [#245](https://github.com/igorrius/ganglion/issues/245)

## Context

R-MEM-1 asked for a nightly consolidation with a model: re-read the day's
turns, extract facts, fix contradictions, delete stale entries, propose a
skill, then drop archived sessions past a retention. R-MEM-2 added a flag
for a disliked turn. The phase plan put this in M7.

Since then M5 built the memory review (R-MEM-8, ADR-0072): a cheap model
reads the owner's quiet private chat once, and every ended session at
once, and stores what it finds, with the owner seeing each change and an
Undo. On 2026-11-21 the owner's node held 6 topics of about 40 lines in
all, with a handful of writes since 2026-11-11.

What nothing does yet is forget. Every ended session stays in the archive
for good; `/resume` and `memory.recall` find them, and `memory.recall`
reads the newest 500 a chat. The open decision in section 11 (owner,
2026-11-03) is a retention after consolidation: nothing is dropped that
has not been read, and what was said by chance fades.

## Options

### A. Retention only, keyed on the review

A daily pass in code, with no model, deletes an archived session that
ended more than `memory.archive_days` ago (default 90, `off` keeps all),
unless the memory review still has to read it: a row in `review_due`
names it. Sessions the review never reads (family members, groups, tasks,
local-only) go by age alone. The nightly model pass (topic clean-up, facts
from tools, the dislike flag) moves to "Not now".

### B. Retention and a nightly clean-up of the topics

A, plus a nightly model call over the topics to merge duplicates, settle
contradictions and drop stale facts, each change shown to the owner with
Undo (R-MEM-9).

### C. All of R-MEM-1

B, plus facts the agent found itself from tools and pages (outside
content, so a Remember or Skip card each time) and a dislike flag that the
nightly pass reads first.

## Comparison

| Criterion | A | B | C |
|---|---|---|---|
| Old sessions fade | yes | yes | yes |
| A model call each night | none | one | one or more |
| What it finds in 6 topics of ~40 lines | n/a | little | little, plus cards to answer |
| Work before the P1 gate | a component and a setting | a prompt, a parser, notes | B plus a flag and cards |

## Decision

A, by the owner's choice (2026-11-21), with 90 days. The review already
turns what the owner says into facts. With memory this small a nightly
model pass would spend a call every night to find almost nothing. 90 days
matches how long the outbox keeps the refs a reply is matched by, so a
reply to an old message and a search of old chats stop finding their
targets at about the same time.

## Security

- **Trust boundary:** none new. The pass deletes rows of the node's own
  database, from code, on a fixed rule.
- **What an attacker controls:** nothing here. The setting lives in
  `ganglion.yaml`, outside `config.propose`'s allowlist (R-CFG-14), so no
  model can shorten the retention to erase a conversation.
- **Abuse case:** a turn makes the agent forget something on purpose:
  only the owner's file sets the age, and the review's pending row keeps
  an unread session.

## Consequences

- Ended sessions older than 90 days vanish from `/sessions`, `/resume` and
  `memory.recall`. Facts in the topics stay, and the bucket's own
  retention keeps old sessions in the backups.
- A review whose event was already put in the inbox but has not run yet
  no longer has a row; a session that is past the age at that moment can
  be dropped before it is read. That needs a node that was off for most of
  90 days, and the review then reads nothing.
- R-MEM-1 and R-MEM-2's nightly part are in "Not now" until memory grows.

## Revisit when

Memory grows to where the owner notices duplicates or stale facts in the
topics, or the agent keeps finding things from tools that the owner wants
remembered.
