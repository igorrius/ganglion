# What an addition costs on the CLI route

- **Date:** 2026-11-05
- **Question:** when a person adds to a request while the agent works, the core can end
  the `claude -p` run and start a new one with the whole history
  ([live turns design](../plans/p1-live-turns-design.md), 3.3). Does that stay within the
  owner's bar of at most twice what two separate turns cost together? Does the prompt
  cache hold across runs? What does writing into a running CLI's stdin do instead?
- **Status:** measured, before anything is built.

**Verdict: ending the run: within the bar.** B ÷ A is 1.06 against a bar of 2.

## Setup

Claude CLI 2.1.318, `--model sonnet` (it ran as `claude-sonnet-5-5`), 3 repeats of each case.

- **The prompt:** `agent.CoreRules` and the example persona, 2.5 KB, about 845 tokens, in
  a file as the node passes it.
- **The history:** about 2,000 tokens of a household's talk, in the form the transcript
  renders it on stdin.
- **The flags:** the node's own: `-p --output-format json --no-session-persistence
  --tools "" --strict-mcp-config --setting-sources "" --system-prompt-file`, in an
  environment holding only what the CLI needs to run and find its login.
- **A marker per repeat** at the top of the system prompt, so a repeat reads nothing that
  an earlier repeat left in the cache.

| Case | Runs |
|---|---|
| A. Two separate turns | Answer Q1; answer Q2 with Q1 and its answer in the history |
| B. An addition | A run with Q1 and one fake tool result in its history; a new run with the same history plus Q2 |
| C. Stdin | `--input-format stream-json --output-format stream-json --verbose`, no tools; Q2 written at the first output of Q1's call |

Every run in A and B was left to finish, so it reported its usage. A real ended run stops
earlier, so B's first run is the worst case of what an ended run can spend.

## A and B

`Cache read` and `Cache write` are `cache_read_input_tokens` and
`cache_creation_input_tokens`. `input_tokens` was 2 in every run, so it has no column.

| Case | Repeat | Run | Cache read | Cache write | Output | Cost, USD | Wall, s |
|---|---|---|---|---|---|---|---|
| A | 1 | Q1 | 845 | 2,287 | 429 | 0.0135 | 5.6 |
| A | 1 | Q2 | 845 | 2,521 | 211 | 0.0123 | 4.0 |
| A | 2 | Q1 | 0 | 3,130 | 522 | 0.0177 | 7.2 |
| A | 2 | Q2 | 845 | 2,518 | 225 | 0.0124 | 4.0 |
| A | 3 | Q1 | 0 | 3,131 | 512 | 0.0177 | 9.9 |
| A | 3 | Q2 | 845 | 2,511 | 223 | 0.0124 | 3.9 |
| B | 1 | Q1 and tool | 0 | 3,355 | 145 | 0.0149 | 3.6 |
| B | 1 | The same and Q2 | 845 | 2,542 | 432 | 0.0146 | 5.7 |
| B | 2 | Q1 and tool | 0 | 3,355 | 128 | 0.0147 | 3.7 |
| B | 2 | The same and Q2 | 845 | 2,542 | 635 | 0.0166 | 15.5 |
| B | 3 | Q1 and tool | 0 | 3,354 | 130 | 0.0147 | 3.6 |
| B | 3 | The same and Q2 | 845 | 2,543 | 527 | 0.0155 | 6.9 |

| Both runs together, mean | A | B | B ÷ A |
|---|---|---|---|
| Cost, USD | 0.0287 | 0.0303 | **1.06** (per repeat: 1.14, 1.04, 1.01) |
| Input tokens, all kinds | 6,497 | 6,746 | 1.04 |
| Output tokens | 707 | 666 | 0.94 |
| Wall time, s | 11.5 | 13.0 | 1.13 |

**Repeat 1 of A is too cheap.** A smoke run just before had the same marker, so A's first
run read 845 cached tokens that it should have written. Repeats 2 and 3 alone give
B ÷ A = 1.02. Keeping repeat 1 errs against ending the run, so the verdict stands either
way.

**B carries more than A.** Its history holds a tool call and result, about 240 tokens,
that A's does not. A real pair of separate turns would hold them too, so the true ratio
is closer to 1.

## Whether the cache held

**Only the system prompt held across runs.** Each new run read the 845 tokens of the
system prompt and wrote the whole transcript to the cache again, about 2,500 tokens, as
the transcript is one block of text on stdin, and a block that changes is not a prefix of
the old one.

This holds for every turn on the CLI route, not only for additions, as
[ADR-0021](../adr/0021-providers-chosen-by-the-operator-claude-cli-included.md) already
recorded, and it is why ending the run costs about what a separate turn costs: neither
reuses the history.

## Stdin (case C)

| Repeat | Q2 written, s | Q1's result, s | Q2 picked up, s | Q2's result, s | Q1's call, USD | Q2's call, USD |
|---|---|---|---|---|---|---|
| 1 | 2.0 | 10.8 | 11.6 | 12.4 | 0.0231 | 0.0059 |
| 2 | 10.5 | 19.5 | 21.4 | 23.3 | 0.0232 | 0.0067 |
| 3 | 1.7 | 10.0 | 10.6 | 11.9 | 0.0229 | 0.0063 |

Times count from when Q1 was written. `total_cost_usd` adds up over the process, so a
call's cost here is the difference between two results. `Q2 picked up` is when the CLI
echoes the message back (`--replay-user-messages`).

- **A message written mid-call does not join that call.** Q1's call ran to `end_turn`
  with no sign of Q2, reported its result, and only then did the CLI take Q2, 0.6 to 1.9 s
  later, as a new model call. This is what a separate turn would do.
- **The cache holds here.** Q2's call read all of Q1's request (about 3,130 tokens) from
  the cache and wrote only what was new. It cost about half of A's second run, though Q1's
  answer was longer here (about 1,060 output tokens against 220 to 520).

So stdin is cheaper per addition, but this run does not show that it would stop a tool
loop, which is what an addition needs.

## A real turn

The `addition-real` scenario, 3 repeats with Sonnet through the node, the real tools and
the bridge: a question, and 2 s later an addition.

| Repeat | What happened | Tokens the runs reported | Replies |
|---|---|---|---|
| 1 | The answer came first: it went out as a quote, then one more run | 17,921 | 2 |
| 2 | The run ended for the addition after `web.fetch`, then one more run | 12,157 | 1 |
| 3 | The same | 12,156 | 1 |

All three answers took both messages into account. Repeat 1 is two runs left to finish, which
is what two separate turns would spend, so it changes nothing against the bar. Repeats 2 and 3
report only the second run, since an ended run reports no usage; what the first run spent is
not seen here, and at most it is what repeat 1 shows. The verdict stands.

## What this does not show

- **A tool boundary on the stdin route.** Case C has no tools, as planned. Whether a
  message reaches the model between two tool calls of one run was not tried, and the
  [headless docs](https://code.claude.com/docs/en/headless) do not promise it.
- **A killed run's bill.** Whether a killed run is billed for the call it was making is
  unknown; a run left to finish is the upper bound.
- **A rate.** Three repeats give the order of the cost, not its spread.
- **Money.** `total_cost_usd` is the CLI's figure at list prices; on a subscription it
  measures use, not a bill.
