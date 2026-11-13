# ganglion docs

Start here when coming back to the project.

## Where we are

- **Phase:** P0 · Recon is complete; P1 · All-in-one is in progress
  ([plan](spec.md#10-plan-phases-and-gates)).
- **Done on 2026-11-13:** Telegram groups (MG), added that day when the
  owner moved the production bot's token to ganglion and removed the old
  bot. A group is connected from a card in the admin's private chat and
  answered only when addressed; nobody has the admin role in a group
  ([plan](plans/telegram-groups.md),
  [ADR-0076](adr/0076-telegram-groups-and-the-addressing-rule.md)). The
  node's file routes a module's messages to named destinations, the node
  knows a forum's topics by title or by their link, the admin routes and
  sends in words with one confirmation, and the built-in skill
  `telegram-chats` tells the agent how
  ([plan](plans/telegram-destinations.md),
  [ADR-0079](adr/0079-a-modules-messages-go-where-the-nodes-file-says.md)).
  Both are deployed and checked live in the family's forum. Asked for
  next by the owner: roles with their own tools for a group's members
  (spec section 14).
- **Next:** M6, the agent reconfigures the system. It now includes the
  encrypted secret store (task 6.5,
  [ADR-0077](adr/0077-encrypted-secret-store-on-the-nodes-key.md), owner,
  2026-11-13) and a webhook mode for the Telegram transport beside
  polling (task 6.6,
  [ADR-0078](adr/0078-telegram-updates-by-polling-or-webhook.md), owner,
  2026-11-13): done the same day: built, checked live on Railway with the
  test bot, and the owner's node switched to the webhook
  ([runbook](deploy-railway.md#telegram-by-webhook)). The Railway deploy
  (board card G-121) is up since 2026-11-11: service `ganglion` beside the
  current bot, on the test bot, the agent through the claude CLI, the
  sandbox ready on kernel 6.18, hard links work on the volume
  ([deploy-railway.md](deploy-railway.md)). Task 4.7 closed the same day:
  `deye-reader` and a `battery-alerts` the agent wrote from scratch run
  there, a build's peak memory is measured, and the owner found the two
  bots' battery messages identical and closed the comparison with
  `deye.py` after one day (owner). M5's review left board card G-140.
- **A reply to a module's message names the module (2026-11-11,
  [ADR-0073](adr/0073-a-modules-message-names-its-module.md), R-DLV-15,
  board card G-141):** found in the first chat on Railway, where the owner
  replied to an alert the local node had sent. A module's message now
  records its module; a reply to it gives the agent the name and none of
  the text, so the session stays clean and `module.write` works; a message
  this node has no record of is said to be so. Moving a node's workspace
  and modules to another host is still by hand (M7).
- **A look at a module no longer closes its rewrite (2026-11-11,
  [ADR-0075](adr/0075-reading-a-modules-state-keeps-the-session-clean.md),
  board card G-147):** the live check of the reply found that one
  `module.status` had tainted the session three hours before, so
  `module.write` refused. `module.status` is now outside content only when
  it shows a setting as text, and `module.write` in a tainted session asks
  the owner, the file then counting as outside content until a clean
  session writes it again. The turn's log line now names the model that
  answered (it named a compaction's model and read as a fallback). A
  rewritten module had been installed as a second `v1`: a new build now
  needs a manifest `version` above the ones the node has had (R-RUN-9,
  board card G-148). The owner's live check on Railway passed for the
  reply, the signature and the rewrite; a `v2` has not been seen yet.
- **A module's name went under its message (2026-11-11,
  [ADR-0074](adr/0074-a-modules-name-goes-under-its-message.md),
  R-RUN-16):** the owner did not want `battery-alerts: ` before the text.
  The node now signs a module's message in a rich message's footer, or as
  an italic last line where there is none; a trial of both went to the
  owner's chat.
- **M5 closed (2026-11-11):** memory by topic
  ([design](plans/p1-m5-memory-topics-design.md),
  [plan](plans/p1-m5-memory-topics.md), ADRs
  [0071](adr/0071-memory-topics-whole-in-the-prompt.md) and
  [0072](adr/0072-who-writes-memory.md) Accepted; board card G-138). Facts
  live in topic files, one line per fact with an id and its source; every
  topic is whole in an admin's prompt within `memory.prompt_budget`, the
  oldest folding first. The agent stores the owner's correction in the
  same turn; a review after 10 quiet minutes, or at a session's end, reads
  the owner's words once (never a tool's result) and stores what the turn
  missed, asking with Remember and Skip in a session that read a page.
  Every write sends "Memory updated" with Undo; `/memory` lists the topics.
  `memory-correction` on the real models: 10 of 10, the agent wrote the
  correction itself in every run and answered "charging" after `/new`
  (442,247 tokens; the full lab run skipped, owner). The owner's live
  check passed and found that the review could store an undone fact
  again (live, Haiku chose not to): a memory button now settles the
  conversation for it. Measured:
  agent with tools 49.5 MB after 1,000 turns (M4c 48.7), binary 48.6 MB
  (48.1); `make crash-agent` 200 kills, 0 duplicates, one commit per fact.
- **M4c closed (2026-11-10):** reflex modules
  ([design](plans/p1-m4c-reflexes-design.md),
  [plan](plans/p1-m4c-reflexes.md), ADRs 0065–0070 Accepted). A module runs
  when cells it listens to change (through the inbox, no model, loops
  refused, chains stop at depth 4); `notify` writes to the owner's private
  chat within a daily limit while the model gets only a node note; the
  agent writes module source with `module.write` from a clean session, and
  a build answer is clean only for files the node wrote and the examples'
  crates; builds and long host commands run as background jobs with a live
  card (R-LIVE-9). Live check: from one message the agent wrote, built and
  offered `battery-alerts`; after the press, at 23:00 `deye-reader`'s cell
  write started it and its alert reached the owner about 0.1 s later with
  no model call. Scheduled tasks may now read modules (`module.status`,
  `module.read`). Open: the board cards G-134–G-139.
- **M4 closed (2026-11-10):** M4a (config, models by purpose) and M4b
  ([plan](plans/p1-m4b-modules.md)) are done and merged; ADRs 0059–0064
  are Accepted. The owner's live check on the test bot passed: the agent
  imported, built and installed `local/deye-reader` through the install
  window, cron wrote the cells without a model call, `/modules`, `/cells`
  and `module.status` work, a second version installed, and the module
  came back after a restart. Memory after 1,000 agent turns with tools is
  about 3 MB above M4a (the owner accepted it). M4 was designed on
  2026-11-09 ([design](plans/p1-m4-reflexes-design.md),
  [phase plan](plans/p1-all-in-one.md)). Load shedding and the two-week
  clock of the gate left P1
  ([ADR-0058](adr/0058-load-shedding-leaves-p1.md)).
- **P0 results:**
  - 0.1 repository skeleton: `cmd/ganglion`, interface packages, CI.
  - 0.2 Deye reader in WASM on wazero: matched `deye.py` on the live API,
    12 of 12 fields.
  - 0.3 module ABI and toolchains: own ABI
    ([ADR-0011](adr/0011-module-abi.md)); Rust and TinyGo modules are 5–20×
    smaller than standard Go.
  - 0.4 MCP and A2A: our envelope over NATS carrying stateless MCP
    ([ADR-0013](adr/0013-protocol-envelope-around-stateless-mcp.md),
    [research](research/2026-11-01-mcp-a2a-coverage.md)).
  - 0.5 runner memory: fits a 128 MB container with room to spare
    ([research](research/2026-11-01-runner-memory.md)); a Pi run later
    confirms arm64 timings.
  - 0.6 Tuya access: moved to P1; loads are state cells with a `virtual`
    backend until the plugs are linked
    ([ADR-0014](adr/0014-state-cells.md)).
  - 0.7 home internet outlasts the inverter; DeyeCloud quota is ample
    ([research](research/2026-11-01-outage-connectivity-and-deye-limits.md)).
- **P1 progress:**
  - M1 state cells done — `cells`, `cells/sqlstore`, `cells/virtual`,
    `sqlitedb` ([plan](plans/p1-m1-cells.md)).
  - M2 delivery and Telegram done — `queue/sqlqueue`, `outbox`,
    `transport/telegram`, `access`, `core`, the core role in `node`
    ([plan](plans/p1-m2-delivery-telegram.md)). An echo bot that survives
    500 `kill -9` without losing a message; 21 MB RSS; run live on the
    test bot. Decisions: [ADR-0018](adr/0018-own-telegram-bot-api-client.md)
    (own Bot API client), [ADR-0019](adr/0019-release-leases-at-core-start.md)
    (leases released at core start, database lock).
  - M3a, the talking agent, done — `provider/openai`, `provider/claudecli`,
    `router/rules`, `turns`, `agent`, core workers
    ([plan](plans/p1-m3a-talking-agent.md)). The bot answers through
    models the operator lists in a catalogue: Haiku through the claude CLI
    first, DeepSeek V4.1 Flash on OpenRouter as the fallback. Sessions
    (`/new`, idle reset, `/sessions`, `/resume`), daily limits, a
    turns trace; 24 MB RSS. Decisions: [ADR-0020](adr/0020-neutral-history-as-conversation-state.md)
    (history as conversation state), [ADR-0021](adr/0021-providers-chosen-by-the-operator-claude-cli-included.md)
    (operator-chosen providers, CLI included), [ADR-0022](adr/0022-haiku-first-during-development.md)
    (Haiku first during development), [ADR-0023](adr/0023-json-schemas-for-yaml-files.md)
    (JSON Schemas for YAML files), [ADR-0024](adr/0024-persona-as-workspace-files.md)
    (persona in workspace files), [ADR-0025](adr/0025-markdown-rendered-to-telegram-entities.md)
    (Markdown rendered to Telegram entities; typing; replies). Run live
    on the test bot by the owner; formatting checked after task 11.
    Closed; the work after the M3a review (persona, schemas, formatting,
    observability) has had no independent review yet.
  - Observability ([plan](plans/p1-observability.md),
    [ADR-0026](adr/0026-opentelemetry-prometheus-and-promtop.md)):
    - one info log line per turn;
    - OpenTelemetry metrics at `http://127.0.0.1:9464/metrics` with the
      promtop dashboard at `/webmetrics/`;
    - OTLP traces when `OTEL_EXPORTER_OTLP_ENDPOINT` is set;
    - Go 1.27.3;
    - the node is 33 MB on disk and 35 MB RSS after 1,000 turns;
    - the R-OBS-8 gaps closed (task 4, 2026-11-03): tool calls and
      policy decisions, confirmations waiting, context size, steps,
      fallbacks, sessions and compactions, module runs, `web.fetch`
      results and sizes, guard verdicts, scheduler runs and lag, inbox
      and outbox depth; duration histograms now have sub-second buckets;
      no measurable memory cost. The owner checked the dashboard on a
      live run (2026-11-03): it works.
  - Refactoring before M3b done ([plan](plans/p1-refactor.md)); no
    behaviour change:
    - the [Go conventions](conventions.md) with lint guardrails
      ([ADR-0027](adr/0027-written-go-conventions-and-lint-guardrails.md));
    - the agent as a pipeline of stages, a command table, tool
      middleware;
    - the node assembled in `config` with `samber/do`; `node` runs roles
      ([ADR-0028](adr/0028-samber-do-composition-root-in-config.md));
    - `ganglion run` and `ganglion version` on urfave/cli, `main.go` at
      the root, development tools in `tools/`
      ([ADR-0029](adr/0029-urfave-cli-for-commands-and-bootstrap-flags.md));
    - 34 MB on disk, 35 MB RSS after 1,000 agent turns.
  - M3b, the agent works with the world, done
    ([plan](plans/p1-m3b-agent-world.md)), merged 2026-11-03. The rest of
    M3 is split in two by the [M3b and M3c design](plans/p1-m3b-m3c-design.md),
    recorded in the [phase plan](plans/p1-all-in-one.md#m3--the-agent-spec-7274-76-77)
    and the [spec](spec.md#102-p1--all-in-one--scope-and-acceptance).
    - **What it brought:**
      - tools under policies and a security baseline
        ([ADR-0031](adr/0031-agent-security-baseline.md);
        [research](research/2026-11-02-agent-security-baseline.md));
      - confirmations and quick actions on buttons
        ([ADR-0032](adr/0032-buttons-and-resume.md));
      - the scheduler with reminders and scheduled agent tasks, moved up
        from M4;
      - memory with a clean-context check
        ([ADR-0033](adr/0033-memory-in-the-workspace-git-with-the-git-binary.md),
        [ADR-0034](adr/0034-clean-context-guard.md));
      - reading the web (`web.fetch` with an `html2md` WASM module in
        Rust, `web.search` through Brave or DuckDuckGo;
        [ADR-0035](adr/0035-web-reading.md));
      - the tool-call journal;
      - the same tools for the claude CLI through an MCP bridge on a
        per-run Unix socket
        ([ADR-0036](adr/0036-mcp-bridge-over-a-per-run-unix-socket.md),
        [ADR-0037](adr/0037-own-minimal-mcp-server.md));
      - carried from M3a: the first-run introduction that writes the persona
        ([ADR-0024](adr/0024-persona-as-workspace-files.md)), the current
        time in the last user message, the depth limit (R-AGT-4) and tool
        placement (R-RTR-3).
    - **Acceptance (2026-11-03):** `make accept` runs the acceptance
      (rates, memory, injection, expiry) against the real core with real
      models and the real web, the fake Bot API playing the owner; it
      passes on both routes. The owner repeats it by hand on the test bot
      with `make live-agent`.
    - **The owner's first live run (2026-11-03)** found seven issues, all
      fixed (rulings R38–R46): no second question for a call already
      waiting, "Open this page?" for `web.fetch`, a toast and a status line
      on every press before its action runs, task runs told their answer
      is delivered, button actions noted in the history, command hints in
      English, Russian and Ukrainian; a `/tasks` command lists the jobs.
      JS-rendered pages went to "Not now"; the bot's language came later
      as fixed texts (3.35). A detailed look at these mechanics was planned
      for the next session; chatlab's scenarios have covered some of them
      since (R38's check among its seeded faults).
    - **Measured (`make memory`):** the binary is 41.9 MB; the node idles
      at 32 MB RSS and is at 39 MB (peak 40 MB) after 1,000 turns that each
      call a tool; one hostile 1 MiB page through `web.fetch` and `html2md`
      peaks the node at 99 MB, under the 128 MB budget.
  - **M3c, done (closed 2026-11-05)**
    ([plan](plans/p1-m3c-modules-skills-diagnostics.md)): module update
    and rollback, skills, attachments and the metrics picture in
    Telegram (`/metrics`), logs on disk, `ganglion mcp` for diagnosis
    from outside, a notice when a file is sent to the agent (R-DLV-10),
    and the 3.8 acceptance under the crash harness. Decisions:
    [ADR-0041](adr/0041-module-install-update-and-trust-boundary.md)–[ADR-0046](adr/0046-a-restart-is-not-a-failed-attempt.md).
    The tag `html2md/v0.1.0` is out. Deferred until the repository opens:
    `/modules update`, `rollback` and the broken release on a live node,
    because a node reads releases anonymously and a private repository
    answers 404.
    - **Acceptance (2026-11-04):** `make accept` passes on both routes.
      `make crash-agent` passes with 200 and with 1,000 `kill -9`: every
      message answered, in order, one commit per fact, every `/metrics`
      with its photo. It found that three restarts in a row dropped a
      healthy message; fixed in
      [ADR-0046](adr/0046-a-restart-is-not-a-failed-attempt.md).
    - **Measured (`make memory`):** the binary is 45.5 MB (M3b: 41.9); RSS
      is 44 MB, peak 44, after 1,000 turns that each call a tool (M3b: 39,
      peak 40). After 50 `/metrics` RSS is 108 MB, peak 114. RSS stays near
      110 MB through 200 pictures in a row, so there is no leak, but the
      peak then reaches 130 MB. Until M4 sets the soft limit (R-RUN-6, task
      4.1), run a 128 MB container with `GOMEMLIMIT=80MiB`: the same 200
      pictures peak at 112 MB (at `100MiB`, 126 MB).
    - **A restart during a turn:** the turn runs again from its start,
      under the same call keys, and the person is not told. Telling them
      and keeping a turn's progress are in the spec's "Not now" list.
    - **The main model (2026-11-04):** the example catalogue is now Sonnet for
      turns and Haiku for compaction and checks
      ([ADR-0047](adr/0047-sonnet-answers-during-development.md)).
      `make accept` passes on it, and runs only the claude CLI route by default.
    - **Chatlab (task 3.31, [ADR-0048](adr/0048-chatlab-over-the-fake-bot-api.md)):** the developer runs the checks the owner could not run by hand:
      a channel-neutral peer talks to a real core over the fake Bot API, facts and
      a judge decide, and a table shows n of n. `bin/chatlab start`, `say`, `press`,
      `advance`, `show` and `stop` hold a conversation from the shell.
      - **What runs:** `make lab` runs by hand the scenarios whose files changed since
        they last passed (`tools/chatlab/passed.json`, in git; `LAB=name` for one). A
        change to the main model, the prompt, the persona or the way to the model
        recommends a full run and never starts one: `make lab-all`, about 900,000
        tokens, only when decided (owner, 2026-11-04), or `make lab-skip-full`.
      - **What needs no model:** the mechanics run on a scripted model in every
        `make test`, and `make lab-faults` proves them on seeded faults for nothing.
        The language of a reply is checked by its letters: 178 of 180 of the judge's
        verdicts agree, and the other two were the judge being too strict.
      - **Seeded faults (2026-11-04):** the policy no longer asking, the notes of R48,
        R49 and R50 removed, and R38's check removed each turned a scenario red; a
        real judge failed wrong replies.
      - **Language:** in chatlab's scenarios the agent lost the language only in
        scheduled tasks, on Sonnet and on Haiku: Russian with a Ukrainian page went
        from 2 of 5 to 10 of 10. The English reply the owner saw after web results
        did not come back in 10 repeats, and stays unexplained
        ([note](research/2026-11-04-language-of-replies.md)).
      - **An expired question closes itself:** the outbox now keeps where a sent
        message went. Review found that Run now's question never closed, and a
        question too long for one message failed its edit: both fixed.
      - **Binary:** nothing of the lab in it: `TestLabStaysOutOfTheBinary`, and
        47,727,550 bytes against 47,735,838 before the lab's commits.
    - **The owner's look in Telegram (2026-11-05):** the `/metrics` picture reads
      once zoomed, the buttons respond, and the notice of a file arrives.
  - **Three gaps closed before the repository opens (task 3.32, 2026-11-05,
    [ADR-0049](adr/0049-three-gaps-where-outside-text-and-private-data-cross.md)):** a read script given input asks in a tainted session; a local-only
    session refuses every tool not declared to keep its data on the node; `ganglion
    mcp` marks its results as data.
  - **Live turns, done (closed 2026-11-05)** ([plan](plans/p1-live-turns.md),
    [ADR-0050](adr/0050-additions-join-a-running-turn.md),
    [ADR-0051](adr/0051-status-reactions-outside-the-outbox.md),
    [ADR-0052](adr/0052-a-plan-is-delivered-during-its-turn.md)):
    - a message written during a turn joins it at the next boundary; an answer it
      overtook goes out as a collapsed quote; on the CLI route this ends the run, at
      [1.06 times](research/2026-11-05-cost-of-an-addition.md) the cost of two turns;
    - reactions show work that takes long, and 👌 an addition taken;
    - a plan for a result to put together, edited in place
      ([when it shows](research/2026-11-05-when-a-plan-shows.md));
    - 1,000 `kill -9` with no message lost, 43.9 MB RSS after 1,000 turns, a 48.0 MB binary;
    - the owner checked it live on the test bot: reactions, additions and the plan.
    - The full chatlab run after the prompt's change was deferred by the owner until after
      the next feature; it ran with fixed texts (2026-11-06) and gave `task-report` and
      `task-selection` their first verdict.
  - **A closed question folds (task 3.33, 2026-11-05, R-POL-9):** one line with the decision and its
    time, and the question under it in a collapsed quote; the owner saw it on the test bot
    (2026-11-06).
  - **`/last` (task 3.34, 2026-11-05, R-OBS-10):** an admin's plain summary of the conversation's last
    turn, with no model: time, outcome, models, steps, tools and how each was decided, the
    messages that joined, tokens and cost. Tools are named, their arguments and results
    never shown. The owner checked it on a live turn that read the agent's logs (2026-11-06).
  - **Fixed texts in the person's language (task 3.35), closed 2026-11-06**
    ([design](plans/p1-fixed-texts-language-design.md), [plan](plans/p1-fixed-texts-language.md),
    [ADR-0053](adr/0053-fixed-texts-in-the-persons-language.md), Accepted; spec 7.13): tests and reviews pass. The owner saw
    a reply follow Telegram's language on the test bot (2026-11-06); a spoken switch and a machine
    translation have not been seen live yet. The full lab passed 32 of 32 on 2026-11-06, for 1,454,519
    tokens, including the first verdicts of `task-report` and `task-selection`. 46.0 MB binary, 44.9 MB RSS after 1,000 turns. The
    `/metrics` memory test (`make memory`) is noisy and went over its 128 MB line on `master` too: peaks
    of 122 to 134 MB on both, so it did not separate them. The line went with
    [ADR-0056](adr/0056-small-hosts-as-a-soft-target.md) (2026-11-08).
  - **Telegram Rich Messages (task 3.36), closed 2026-11-06**
    ([design](plans/p1-rich-messages-design.md), [plan](plans/p1-rich-messages.md),
    [ADR-0054](adr/0054-structured-replies-as-rich-blocks-the-transport-builds.md), Accepted): a message with a
    table or at least two headings goes out as a rich message from an allowlist of blocks, a refusal goes the
    ordinary way, a symbol tree keeps its levels and its links, and a rich message sent to the bot is read as outside content.
    Tests, the review and the five lab scenarios it touched pass; 46.1 MB binary, 44.2 MB RSS after 1,000
    turns. The owner's look on the test bot (2026-11-06): the table fits a phone, the long report comes as one message,
    a reply works, a tree of links keeps its levels and links in both kinds of message, and the bot's own rich answer
    forwarded back is read and taints the session. Telegram refused nothing. An empty table cell has not been seen
    live; a refusal of it would go the ordinary way.
  - **Knowing which message a reply answers (task 3.37), closed 2026-11-07**
    ([design](plans/p1-reply-context-design.md), [plan](plans/p1-reply-context.md),
    [ADR-0055](adr/0055-a-reply-carries-the-replied-messages-origin.md), Accepted; R-DLV-13, R-DLV-14):
    - a reply to the bot's messages or the owner's own, up to 90 days old, tells the agent what it answers:
      a short pointer for a message of the current session, the text for any other;
    - every message the node sends records its origin (session, taint, kind); `outbox_refs` keeps every
      delivered part's ref with a copy of the text and origin for 90 days, while the outbox keeps 7;
    - the taint follows that origin: a reply to a clean message taints nothing, so a quote of the bot's own
      clean answer no longer taints; a tainted origin, no origin, a forged copy, a forward or a failed lookup
      taints; the taint is decided before "current session";
    - a forum topic's root is not a reply.
    - Tests, the task reviews and the whole branch's review pass. Chatlab: `mech-reply-origin` 3 of 3 on the
      scripted model, `reply-topic` 3 of 3 on the real model, and the seeded fault `reply-origin-taint` turns
      it red. `make crash-agent` holds with 200 `kill -9`; 46.2 MB binary, 45.5 MB RSS after 1,000 turns
      (Rich Messages: 46.1 and 44.2).
    - The full lab (owner's call, 2026-11-07): 33 of 34 passed, 1,576,142 tokens. `language-ru-task` was
      flaky: one of five Russian answers kept the Ukrainian word "сьогодні" from the page it read, the known
      weakness of scheduled tasks' language, which replies do not touch.
    - The owner's look on the test bot (2026-11-07): a reply "and in the west?" to a weather answer was
      answered for Lviv; a reply to a task report that read the web knew the rates and asked before writing
      memory (tainted); a reply to a clean answer wrote memory with no question (`ganglion_replies`: one
      `earlier`, two `tainted`). The memory check then held back that fact, a sum, as not about the owner,
      which is its own rule. Seen on the way: two presses for two currency pages and a third for the same
      weather site with another city (in "Not now", security first); the first-run introduction did not
      run because the workspace was copied from the example persona.
  - **Memory is a target, not a line (2026-11-08,
    [ADR-0056](adr/0056-small-hosts-as-a-soft-target.md)):** principle 8 aims at small hosts, held by
    `GOMEMLIMIT`; each milestone compares its numbers with the previous one; a feature that does not fit
    a small host runs on a denser node. The memory tests report and no longer fail above 128 MB.
    Setting `GOMEMLIMIT` from the container's limit is M4 task 4.1.
  - **`/status` (task 3.38), closed 2026-11-08**
    ([design](plans/p1-status-design.md), [plan](plans/p1-status.md), R-OBS-11):
    - an admin's `/status`, with no model and no note in the history: a verdict, then the node (the full
      version, uptime, memory against `GOMEMLIMIT`, the database and logs), the models (this session's,
      each model's last answer and failure, the day against the limits and the spend), delivery
      (Telegram's last poll, the queues) and what is off (skills not loaded, a module fallen back, failed
      turns, the scheduler), in the person's language;
    - every component reports through `status.Source`, registered in `config` with one line; a reason is
      a fixed text, never an error's text;
    - tests, lint and the whole branch's review pass, its findings fixed (a compaction read as a
      fallback, a module warning that never cleared, sources' failures not logged). Chatlab:
      `mech-status` 3 of 3 on the scripted model; the seeded fault `status-verdict` turns it red;
    - measured (`make memory`, ADR-0056): 46.4 MB binary (replies: 46.2); the agent with tools at
      47–48 MB after 1,000 turns against 44–46 MB on `master`, but 46–49 MB with the sources switched
      off, and runs of the same code differ by 3 MB: within the measure's noise;
    - the owner's look on the test bot found three things, fixed: the job count now matches the list
      (a one-time job that ran is not counted), a catch-up run after a restart is not late, and
      lengths of time are written in the reader's units from the catalogue, in Go with no model call;
    - no full lab run: the branch does not touch the turn's stages (owner, 2026-11-08).
  - **The command sandbox (task 3.20), closed 2026-11-08** on branch `m3d-sandbox`
    ([plan](plans/p1-m3d-sandbox.md), [spike](research/2026-11-08-command-sandbox-spike.md),
    [ADR-0039](adr/0039-command-sandbox.md) accepted, R-CMD-3):
    - the node starts its own image as `ganglion __sandbox` with a clean environment; the helper sets
      `no_new_privs`, confines itself with Landlock, refuses Unix sockets with seccomp below
      Landlock 9 and execs the command; the node's environment and memory stay closed because it is
      not dumpable, and `ps` still sees the machine (owner);
    - denied for each command: the node's binary, data, logs, modules and workspace, `*_FILE`
      secrets, the owner's secret stores, shell histories and the files that run at login,
      `.env` files three folders deep, `sandbox.deny`; 59 paths on the owner's machine;
    - checks at start and a self-test; off with a reason in `/status` and the log (not Linux, root,
      no Landlock, dumpable, self-test); signals not limited, so `kill` will reach the owner's
      processes (owner);
    - a review broke the first build four ways (a store made after start, the helper binary
      replaced, a `.env` symlink, Unix sockets to gpg-agent and the keyring); each is closed with an
      abuse test;
    - measured: binary 46.4 MB, as on `master`; the agent with tools 44.7 MB after 1,000 turns;
    - open: Railway and a Pi not measured (`tools/sandboxprobe`); hard links and bind mounts made
      before are out of reach of path rules; a `.env` deeper than three folders stays readable;
      Windows and macOS have no commands ("Not now");
    - the owner's look on the test bot: "✅ Command sandbox: ready, Landlock 7".
  - **M3d, done (closed 2026-11-09)** on branch `m3d-commands`, merge pending
    ([plan](plans/p1-m3d-commands.md),
    [design](plans/p1-m3d-host-commands-design.md), spec 7.12, R-CMD-1…8). The owner accepted the plan,
    [ADR-0038](adr/0038-host-commands-as-argv-with-classes-in-code.md) and
    [ADR-0040](adr/0040-plans-as-approvals.md) on 2026-11-09:
    - `host.stats`: memory, load, disk and the node's own files, with no subprocess; the node's places on
      disk are one list in `config`, and a janitor removes what is past its age;
    - `host.exec` as argv in the command sandbox, under the class table in code (`hostcmd`): read runs at
      once, content, change and script ask with a fixed line in three languages, forbidden never runs, and
      what the table cannot read moves up; output capped, redacted and outside content;
    - a command past 20 s goes on in the background and its end resumes the conversation (a task's under
      its grant); a command runs once per call key across a rerun and a restart;
    - plans as approvals: one press on a plan approves exactly the commands it lists, in its conversation,
      for an hour; commands in a task's grant, listed with their folders and lines;
    - skill scripts run in the command sandbox, from a copy of their skill, and only while it is on;
    - the kill switch `tools.host_exec` and an admin's `/commands on|off`, with its line in `/status`;
    - commands may list the folders the sandbox splits; `sandbox.hide_names: true` hides the names
      ([ADR-0057](adr/0057-commands-may-list-split-folders.md));
    - scripted acceptance: seven mech-host chatlab scenarios, 3 of 3 runs each, the end-to-end sandbox test
      (`config/hostexec_accept_test.go`) and the task-grant test; the branch's reviews found gaps in the
      class table (awk options, `env`, `xargs`, `find -exec`) and each is closed with a test;
    - measured (`make memory`, `make crash-agent`, ADR-0056), against `/status`'s 46.4 MB binary and
      47–48 MB RSS and the sandbox's 44.7 MB: binary 47.0 MB (+0.6); the agent with tools 45.1 MB idle,
      47.4 MB after 1,000 turns (peak 48.5); the agent with `host.exec df -h` in every turn through the
      real sandbox 45.1 MB idle, 50.7 MB after 1,000 turns (+3.3 MB over the tools run, a helper process
      per call); echo 39.2 and 44.0 MB; the agent without tools 45.2 and 48.9 MB; 50 `/metrics`
      pictures 109 MB, the hostile page peak 111 MB, as before. `make crash-agent`: 200 `kill -9`, no
      message lost, no duplicate: 246 messages for the model in 150 groups (96 joined a running turn),
      157 answers. The crash harness does not call `host.exec`, so the
      `host_jobs` restart path is covered by the runner's tests, not by it;
    - full lab run 2026-11-09: 42 of 42 passed, about 1.86M tokens. `make lab-all` ran 39 and stopped at its
      1.5M-token cap; the other three (reply-topic, task-report, task-selection) passed when run on their
      own. Reports: `bin/lab/20261109-065838` and `bin/lab/20261109-071516`;
    - the live check on @ganglion_test_bot, 2026-11-09, run by Claude through the owner's Telegram Web at
      the owner's request: all eight steps passed:
      1. `host.stats` and `du` ran with no button and the tree was shown; `du -sh ~` works
         ([ADR-0057](adr/0057-commands-may-list-split-folders.md)); a `sleep 30` went to the background and
         `command.done` resumed the chat;
      2. one press on the plan approved `go clean -cache` and `rm -rf` (no more presses): the cache went
         from 15G to 12K and the folder was removed;
      3. `rm -rf` of another folder outside the plan got its own question;
      4. `sudo true` was refused with no button;
      5. `cat ~/.ssh/config` asked with the content line; after Approve it failed with Permission denied
         (sandbox);
      6. `/commands off` removed `host.exec`, `/status` said "Commands: switched off with /commands", `df`
         did not run, and `/commands on` restored it;
      7. a task created with commands showed their lines in the question; Run now asked; the run was
         allowed by the grant (the folder differed from the grant's `""`, and the match held);
      8. a 128 MB container as root (static build, `debian:stable-slim`, `GOMEMLIMIT=80MiB`) logged
         "sandbox: commands are off reason=root", `/status` showed the sandbox off because the node runs as
         root, memory 43 MB; `host.stats` through a model could not be checked there (no claude CLI in the
         container), and `TestHostStatsWithoutTheSandbox` covers it;
    - the live check found a bug the tests had missed: with a relative database path the claude CLI's work
      folder was relative, so the CLI could not find its system prompt and every model call failed. Fixed in
      6821fb4 (an absolute work folder) with tests;
    - open: Railway and a Pi are still to measure; Docker does not work from commands below Landlock ABI 9
      (commands are classed and asked for as designed, then fail to reach the daemon); `make lab-faults` was
      not run; the merge.
  - **M4a, config and models by purpose, done (2026-11-09)** on branch `m4-reflexes`, checked live on
    the test bot by the owner (turns, `/status`, memory, `/restart` with exit 75) ([plan](plans/p1-m4a-config.md), tasks 4.1 and 4.2,
    [ADR-0059](adr/0059-node-config-file.md), [ADR-0060](adr/0060-models-by-purpose.md)):
    - one config file, `ganglion.yaml`, with `schemas/node.schema.json`; a secret is written as `env:NAME`,
      never in the file; parsing is strict and a bad file fails the start naming the path;
    - the retired variables (`GANGLION_TZ` and the rest) fail the start with a hint to the new key;
    - models by purpose (`chat`, `check`, ...), each with an order and an effort; the tools and the live
      scripts read example files;
    - `GOMEMLIMIT` comes from the environment, `node.memory_limit` or the container's cgroup, in that order;
      in a 256 MB container with no setting it is 214,748,364 bytes (`source=cgroup`), with
      `GOMEMLIMIT=100MiB` `source=env`, with `memory_limit: 120MiB` `source=config`;
    - `/restart` ends the node with exit code 75 for its supervisor to start again;
    - measured (`make memory`, ADR-0056), against M3d: binary 46.9 MB (47.0); the agent with tools 39.2 MB
      idle, 44.8 MB after 1,000 turns (45.1 and 47.4); with `host.exec` 39.4 and 45.8 MB (45.1 and 50.7);
      echo 41.6 and 43.4 MB (39.2 and 44.0); the agent without tools 44.8 and 48.1 MB (45.2 and 48.9);
      50 `/metrics` pictures 110 MB, the hostile page peak 111 MB, as before: no growth over 2 MB;
    - open: the owner's live check on the test bot (a turn, `/status` models in the `chat` order,
      `/restart` under a supervisor, the check on Haiku).
  - Scope stays one operator, one agent, one bot (spec 14). Voice
    messages and PDF generation went to "Not now".
  - Deferred minors from the reviews:
    - M2: record a send's outcome with `context.WithoutCancel` on
      shutdown; keep the seeded admin's name and plan admin removal; do
      not end the node when the `409` alert cannot be stored.
    - M3a: move to goldmark v2 (`github.com/yuin/goldmark/v2`), where
      upstream now lands some fixes first; remove or document `provider.Fallback`; clean up the claude
      work dir; name people, not ids, in limit alerts.
    - M3b: prune the `pending_calls`, `pending_buttons` and `cron_runs`
      tables; scope `cells.set` grants per cell (M4); a URL becomes
      known from any speaker's message, not only the owner's;
      `message.send` reaches a known conversation on the calling
      transport only; one poison `unput` row stops the scheduler's tick
      for every job; `EnsureAudit` takes the first admin at start, not
      when the first admin changes; episodes are a substring scan of the
      newest 500 sessions, not full-text search; the M4 policy override
      against the tainted-memory rule; the html2md worst case peaks at
      99 MB; transcript indentation covers only the
      line breaks it lists; `web.WithResolver` and `web.WithConnect` are
      exported test seams; no automated depth check of htmd's own
      html5ever 0.38 tree; the nightly audit runs inside one event,
      under the core's turn timeout (batch the guard calls); nothing
      locks the workspace to one node (removing a stale `index.lock`
      assumes it); a crash can leave a quarantined copy's deletion
      uncommitted; a Telegram display name can read like the owner's in
      the speaker prefix; an approved page is capped like a tool result
      while an auto-allowed one is not.
    - M3c: an admin alert at start when a stored module falls back or a
      skill is not loaded (both are logged and visible otherwise); pin the
      GitHub Actions by commit SHA in `modules-release.yml`; prune old
      failed `module_versions` rows; `/modules update` blocks its turn up
      to about 60 s plus the self-test; a script outlives a killed node, and
      its temp dir stays; log files past their age stay until the next
      rotation or start; a failed reopen stops disk logs until a restart;
      "no release found" covers a channel that cannot be read and a release
      for a newer core; `metrics.show` does not check the attachment cap
      itself; a stored module shadows a newer built-in with no word of it;
      an album costs one model turn per photo; a script's stdout is not
      redacted as its stderr is; ADR-0041's host list omits
      `release-assets.githubusercontent.com`; the `/metrics` picture cannot be
      read without zooming, on a phone or on a desktop, so a short summary could
      come first; a file sent with no text is answered in English, since the
      message gives no language to follow.
    - Rich Messages:
      - rich blocks Telegram may refuse, which then go the ordinary way: an empty list item or quote, an empty
        link or heading, a link inside a link (a badge); the fake accepts them;
      - an ordered list from 0 loses its first number, since `value` is left out at zero;
      - a tree inside a quote loses one column on its deeper lines;
      - the last line of a multi-line raw HTML block is not shown, in both renderers (older than this work);
      - link addresses are sent with their escapes unresolved, in both renderers;
      - inline formulas and custom emoji in a rich message sent to the bot are not read;
      - `Parts` parses repeatedly: 1.8 s for a 1,000-row table, 9.5 s for 5,000 rows, in the one sender goroutine;
      - an ordinary message still makes a link from any scheme, `tg://` included;
      - a table inside a quote or a list above the limits is cut by length, without its header;
      - deploying this, with its default on, while a split message is in flight may skip or repeat a part.
    - `/status`:
      - the models' lines are those seen in the last week, not the catalogue's: a model taken out of the
        catalogue warns for a week, and one never used shows nothing;
      - after a `409` polling stops, so `/status` cannot reach the bot over Telegram; its line is for
        another transport, or for after a restart;
      - the queues' line shows one reason: a message waiting too long hides the undelivered count;
      - a section outside the four is sorted last and not shown;
      - no config-level test that the list holds Telegram's source exactly when Telegram is on;
      - `make lab-faults` as a whole and a full lab run wait for the owner (chatlab recommends a full run
        for `agent/stages.go`, which this branch did not change).
    - Replies (ADR-0055):
      - a reply to an attachment sent without a caption, or to a piece of a table the transport cut into
        several messages, is not found and counts as outside content;
      - an approved parked call after `/new` is stored with the new session's clean origin (the owner approved
        its text); a reply to a folded question reads its status line, not the question;
      - alerts, the give-up reply and notifiers store no origin, so a reply to them taints;
      - no test of the CLI tool path's origin or of the early quote and acknowledgement;
      - `ganglion.replies` counts again on a retried turn;
      - after a crash the last part of a split message may be sent again (at least once, as before);
      - `noteRef` reads the whole outbox body, attachment bytes included, to copy the text;
      - a tainted replied message with only a file has no "a message with a file" mark.
    - M3d (host commands):
      - the janitor's symlink test does not check the link itself is gone, nor a symlink in an old folder;
      - an aged place with an empty prefix would empty its whole folder: no guard against such a place;
      - `Sweep` does not check its context inside the loop; `Lstat` and `RemoveAll` are not one step;
      - `host.stats` prints ages as Go durations (`24h0m0s`) and "1 files";
      - the `/proc` parsers take NaN, Inf and negative numbers, and a duplicated meminfo line;
      - `Disk` multiplies by `Bsize`, while statfs counts in `Frsize`; `mergeTemp` aliases its input;
      - `ParseExec` takes trailing JSON and keys that differ only in case;
      - `ParsePlan` has no step or reason limits of its own (plan.show's question now runs the store's);
      - the timeout's error says 1..900 while 0 is taken; `python3 -c` imports modules from its folder;
      - other interpreter names (`python3.12`, `ruby`) are change, not script from a file;
      - `top -b` reads `~/.toprc`;
      - `tar -cf host:/x` goes through rsh or ssh; `tar --remove-files` shows the archive line;
      - `journalctl --cursor-file` writes a file; `docker image history --no-trunc` prints build commands as read;
      - other wrappers (`setpriv`, `prlimit`, `numactl`, `cgexec`, `setarch`, `faketime`) are unknown, so change;
      - `Show` passes graphic runes that look blank (U+3164, U+115F, U+2800, combining overlays);
      - `date --rfc-3339=seconds` is over-classified as root; `mawk -W version` and `awk -vfoo=1` are forbidden;
      - `gawk -o`, `-p` and `-d` write named files and are script, not create;
      - a job's `Answer` may read done while `Done` is false; the time-limit test does not prove grandchildren die;
      - the `host_jobs_open` partial index cannot serve `state != 'running'`; `ended` is not counted when the end fails;
      - an exit 0 at the node's stop is stored as interrupted;
      - `redact.String` has no pattern of its own for `sk-…`, `ghp_…` or bare tokens; a secret of 256+ bytes across a cut;
      - `Ignored` runs up to 63 arguments × 5 s in turn; it stats the model's paths outside the sandbox;
      - the agent's reason line is not escaped; the git-ignored warning misses `--file=path` and symlinks;
      - a failed `/commands` write fails the turn; the `tools.*` abuse test covers only `tools.host_exec`;
      - no test that `host.exec` closes with the real switch off; `commandGate` is not tested in `config`;
      - a given-up background key stays up to 7 days and keeps the plan open (its approval still ends at its hour);
      - `commandDone` checks neither the job's background flag nor its state; `commandAsTask` neither kind nor conversation;
      - `/resume`'s running filter repeats the race; migration 007 has no ADR;
      - the give-up apology for a person's `command.done` is in the default language;
      - the class predicates differ (`!= Read` in the policy and `planAllowed`, `NeedsPress` elsewhere);
      - a plan step's text is not marked as the agent's words in its question;
      - the out-of-grant alert names the argv but not the folder; a task's summary drops the git-ignored warning;
      - `sameJob` ignores the grant (older than M3d);
      - a skill's copy is capped before it is copied; the skill abuse test skips silently without Landlock;
      - `TMPDIR` is assumed outside the deny list; a failed start after `Begin` leaves its key begun;
      - `mech-host-forbidden` counts two forbidden calls without telling `sudo` from `systemd-run`;
      - the injection scenario's second facts repeat the first; the background scenario's second reply proves little;
      - a long doc line in `grants.go`;
      - the Landlock write checks assert only a non-zero exit.
      - from the live check (not M3d code): the agent asked two separate questions instead of showing a plan
        until told to, and did not tick the plan's steps after the work (the plan is a prompt directive);
      - from the live check: fixed texts alternated between English and Ukrainian although the person's
        language is `en` and none is chosen (the language subsystem, task 3.35);
      - from the live check: a path like `/tmp/ganglion` in a plan step's text or the agent's reason renders as
        a bot-command link in Telegram (step text and reason are not escaped);
      - from the live check: the fallback MCP socket dir under a relative `TMPDIR` stays relative
        (pre-existing).
- **Open decisions:** [spec section 11](spec.md#11-open-decisions)

Update this block when a task or phase closes.

## Owner setup for live checks

What the owner sets up by hand before the first live run on the test bot
(M2 task 8 of the [M2 plan](plans/p1-m2-delivery-telegram.md)). Nothing here
is needed for tests: they run against a fake Bot API.

- **Test bot:** @ganglion_test_bot, used only by ganglion (since
  2026-11-02; the development bot has another project's webhook). Its
  token is `TELEGRAM_TEST_BOT_TOKEN`. Two runs at once get `409 Conflict`,
  and the older one stops polling (R-DLV-8).
- **Your Telegram user id:** needed for the admin entry in the access list
  (`telegram.admin: telegram:user:<id>` in the settings file); strangers get no reply. Any
  "user info" bot shows it, or the `from.id` of your message in
  `getUpdates`.
- **Running locally:** a `.env` file in the repository root (git-ignored); copy
  [`.env.example`](../.env.example), which also lists the optional variables:

  ```sh
  TELEGRAM_TEST_BOT_TOKEN=<token from @BotFather>
  ```

  Settings go in `ganglion.local.yaml` (git-ignored): copy
  [`ganglion.example.yaml`](../ganglion.example.yaml) and set `telegram.admin` to
  `telegram:user:<your id>`. For `make live-echo`, set it in
  [`examples/ganglion.echo.yaml`](../examples/ganglion.echo.yaml) instead.

- **Running in a Claude Code cloud session:** a local `.env` does not reach
  the container. In the cloud environment's settings (the environment menu
  in the session title bar → Edit):
  1. Network access: add `api.telegram.org` to Allowed domains, keeping
     "Allow package managers" ticked
     ([docs](https://code.claude.com/docs/en/cloud-environments#network-access)).
  2. Add `TELEGRAM_TEST_BOT_TOKEN` as a secret or environment variable. A new session picks it up.
     Never paste the token into the chat. Settings go in a `ganglion.yaml` the session can read.

## Installing a skill

A skill is a procedure the agent reads before it follows it
([ADR-0042](adr/0042-skills-and-scripts-before-the-sandbox.md)). Only you
install one; the agent may propose the text.

1. Make `workspace/skills/<name>/SKILL.md`: a frontmatter with `name`
   (the directory's), `description`, optionally `model` and `script`,
   then the steps in Markdown.
2. A script sits beside it, executable, named by `script.run`, with
   `effects: read` (runs by itself) or `effects: change` (asks with a
   button). In a session that has read outside content, such as a web page, a
   read script given input asks too, since it may reach the network. It gets
   only `PATH`, `HOME`, `LANG`, `TZ` and the names in `script.env`; the node's
   secrets never.
3. Restart the node; the log names a skill that was not loaded and why.

Until the sandbox of M3d, a script can read whatever the node's user can
read, the database and secret files included: install only scripts you
trust with that.

## Language files

What the bot writes without a model follows each person's language
([ADR-0053](adr/0053-fixed-texts-in-the-persons-language.md)): the one they asked for in the chat (for
now only an admin can ask), then their Telegram's, then English. English, Russian and Ukrainian ship in the binary; any other
language in the table is translated once by the cheap model, and each person is told so once.

To correct a text, put `<code>.yaml` in the `languages/` directory beside the database
(`agent.languages` moves it, but never into the workspace), with the keys you want from
`texts/lang/en.yaml`, and restart. A text keeps its `{placeholders}` and adds no links, mentions or
Markdown; one that does not is logged and left out. Your file comes before a shipped file and a
translation, key by key.

## Releasing a module

A built-in module ships in the binary and is updated on a running node
without a new core ([ADR-0041](adr/0041-module-install-update-and-trust-boundary.md)):

1. Change `modules/html2md`, run `make modules-html2md`, commit the
   rebuilt `html2md.wasm` with its sources.
2. Push a tag `html2md/vX.Y.Z`. The `Module release` workflow rebuilds
   it, checks it matches the commit, runs the fixtures and the self-test,
   and publishes the `.wasm`, the manifest and `SHA256SUMS`.
3. On the node: `/modules update html2md`; `/modules rollback html2md`
   goes back; `/modules` lists the versions and why one was refused.

A release that asks for a capability, more memory or time than the
core's ceiling, or another ABI, is refused until the core is upgraded.

## Diagnosing the node from outside

`ganglion mcp` serves read-only diagnostics (logs, the tool-call journal,
failed turns, one turn whole, the queues, the settings) to Claude Code or
any MCP client, with no running node and no model
([ADR-0044](adr/0044-ganglion-mcp-diagnostics-over-stdio.md)). Add it to
the client's MCP configuration (`.mcp.json` for Claude Code):

```json
{"mcpServers": {"ganglion": {"command": "ganglion", "args": ["mcp", "--db", "/path/to/ganglion.db"]}}}
```

- **Another machine:** `"command": "ssh", "args": ["host", "ganglion", "mcp", "--db", "/data/ganglion.db"]`.
- **A container:** `"command": "docker", "args": ["exec", "-i", "<container>", "ganglion", "mcp", "--db", "/data/ganglion.db"]`;
  `scripts/diag-container.sh <data dir>` starts a local one to try it.
- Logs are read from `logs/` beside the database; `--log-dir` points
  elsewhere.

Whoever can run the command can read all conversation history in the
data directory: give it to the people you would give the files to.

Logs and the journal quote web pages. Results come marked as data and the server
tells its client so, but that is a convention the client's model follows, not a
boundary. Do not run diagnostics in a session that approves commands by itself
([ADR-0049](adr/0049-three-gaps-where-outside-text-and-private-data-cross.md)).

## Map

| Question | Read |
|---|---|
| What are we building, what is in scope now, what are the requirements? | [spec.md](spec.md) |
| What does the system look like, from the big picture down to packages? | [architecture/](architecture/README.md) |
| Why was it decided this way, and what else was considered? | [adr/](adr/README.md) |
| Where is X in the code, and does it exist yet? | [architecture/05-code-map.md](architecture/05-code-map.md) |
| What did we find out before deciding? | [research/](research/) |
| How do we write Go code here? | [conventions.md](conventions.md) |
| What is the plan for the current phase? | [plans/](plans/) |

Suggested path after a long break: this page → the
[one-picture overview](architecture/README.md#the-idea-in-one-picture) →
[containers](architecture/02-containers.md) → the current phase in the
[spec](spec.md#10-plan-phases-and-gates) → the [code map](architecture/05-code-map.md).

## Deploy on Railway

The image is built from the [`Dockerfile`](../Dockerfile): the node, Rust with
`wasm32-wasip1` in `/opt/rust`, an unprivileged user `ganglion` (uid 10001)
the claude CLI, and a volume at `/data` that holds `ganglion.yaml` and the
database. The
owner sets up the service, the volume, the variables (`TELEGRAM_BOT_TOKEN` of
the test bot, `DEYE_*`, a provider key) and the restart policy by the
[runbook](deploy-railway.md). Module builds need Linux 6.2 or later (Landlock
ABI 3).

## Reading in Obsidian

The `docs/` folder works as an Obsidian vault: graph view, backlinks and
quick switching come for free, and Mermaid diagrams render natively.

1. Open `docs/` as a vault ("Open folder as vault").
2. Settings → Files and links: turn off **Use [[Wikilinks]]** and set
   **New link format** to **Relative path to file**, so new links keep
   working on GitHub ([ADR-0002](adr/0002-docs-as-markdown-and-mermaid.md)).

`docs/.obsidian/` is git-ignored, so vault settings stay local.
