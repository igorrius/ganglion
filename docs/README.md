# ganglion docs

Start here when coming back to the project.

## Where we are

- **Phase:** P0 · Recon is complete; P1 · All-in-one is in progress
  ([plan](spec.md#10-plan-phases-and-gates)).
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
  - M3a, the talking agent, built — `provider/openai`, `provider/claudecli`,
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
- **Done:** M3b, the agent works with the world
  ([plan](plans/p1-m3b-agent-world.md)), merged 2026-11-03. **M3b: acceptance scripted
  (2026-11-03); the owner's first live run on the test bot (2026-11-03)
  found seven issues, all fixed (rulings R38–R46): no second question
  for a call already waiting, "Open this page?" for `web.fetch`, a toast
  and a status line on every press before its action runs, task runs
  told their answer is delivered, button actions noted in the history,
  command hints in English, Russian and Ukrainian; JS-rendered pages and
  the bot's language went to "Not now"; a `/tasks` command lists the
  jobs. The owner merged it and checks these mechanics in detail at the
  start of the next session.**
  `make accept` runs
  the acceptance (rates, memory, injection, expiry) against the real
  core with real models and the real web, the fake Bot API playing the
  owner, once with Haiku through the claude CLI and once with DeepSeek
  V4.1 Flash on OpenRouter; both pass. The owner repeats it by hand on
  the test bot with `make live-agent`. Measured (`make memory`): the
  binary is 41.9 MB; the node idles at 32 MB RSS and is at 39 MB (peak
  40 MB) after 1,000 turns that each call a tool; one hostile 1 MiB
  page through `web.fetch` and `html2md` peaks the node at 99 MB,
  under the 128 MB budget. The rest of M3 is split in two by
  the [M3b and M3c design](plans/p1-m3b-m3c-design.md), recorded in the
  [phase plan](plans/p1-all-in-one.md#m3--the-agent-spec-7274-76-77) and
  the [spec](spec.md#102-p1--all-in-one--scope-and-acceptance):
  - **M3b:**
    - tools under policies and a security baseline
      ([ADR-0031](adr/0031-agent-security-baseline.md), Proposed;
      [research](research/2026-11-02-agent-security-baseline.md));
    - confirmations and quick actions on buttons;
    - the scheduler with reminders and scheduled agent tasks, moved up
      from M4;
    - memory with a clean-context check
      ([ADR-0033](adr/0033-memory-in-the-workspace-git-with-the-git-binary.md),
      [ADR-0034](adr/0034-clean-context-guard.md), Proposed);
    - reading the web (`web.fetch` with an `html2md` WASM module in
      Rust, `web.search` through Brave or DuckDuckGo;
      [ADR-0035](adr/0035-web-reading.md), Proposed);
    - the tool-call journal;
    - the same tools for the claude CLI through an MCP bridge on a
      per-run Unix socket
      ([ADR-0036](adr/0036-mcp-bridge-over-a-per-run-unix-socket.md),
      [ADR-0037](adr/0037-own-minimal-mcp-server.md), Proposed);
    - carried from M3a: the first-run introduction that writes the persona
      ([ADR-0024](adr/0024-persona-as-workspace-files.md)), the current
      time in the last user message, the depth limit (R-AGT-4) and tool
      placement (R-RTR-3).
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
    - **Chatlab (task 3.20, [ADR-0048](adr/0048-chatlab-over-the-fake-bot-api.md)):** the developer runs the checks the owner could not run by hand:
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
    - The full chatlab run after the prompt's change is deferred by the owner until after
      the next feature; `task-report` and `task-selection` have their first verdict then.
  - **M3d, after M3c (designed 2026-11-03):** host commands and plans
    ([design](plans/p1-m3d-host-commands-design.md), spec 7.12):
    `host.exec` as argv under a class table in code, in a sandbox that
    fails closed; `host.stats`; a plan message whose one press approves
    exactly its commands; commands in task grants; the node accounts for
    its own files. [ADR-0038](adr/0038-host-commands-as-argv-with-classes-in-code.md),
    [ADR-0039](adr/0039-command-sandbox.md),
    [ADR-0040](adr/0040-plans-as-approvals.md), Proposed. Reading files
    and pictures sent to the agent is its own brainstorm after M3d.
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
      99 MB of the 128 MB budget; transcript indentation covers only the
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
  - **Three gaps closed before the repository opens (2026-11-05,
    [ADR-0049](adr/0049-three-gaps-where-outside-text-and-private-data-cross.md)):** a read script given input asks in a tainted session; a local-only
    session refuses every tool not declared to keep its data on the node; `ganglion
    mcp` marks its results as data.
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
  (`GANGLION_ADMIN=telegram:user:<id>`); strangers get no reply. Any
  "user info" bot shows it, or the `from.id` of your message in
  `getUpdates`.
- **Running locally:** a `.env` file in the repository root (git-ignored):

  ```sh
  TELEGRAM_TEST_BOT_TOKEN=<token from @BotFather>
  GANGLION_ADMIN=telegram:user:<your id>
  ```

- **Running in a Claude Code cloud session:** a local `.env` does not reach
  the container. In the cloud environment's settings (the environment menu
  in the session title bar → Edit):
  1. Network access: add `api.telegram.org` to Allowed domains, keeping
     "Allow package managers" ticked
     ([docs](https://code.claude.com/docs/en/cloud-environments#network-access)).
  2. Add `TELEGRAM_TEST_BOT_TOKEN` and `GANGLION_ADMIN` as secrets or
     environment variables. A new session picks them up. Never paste the
     token into the chat.

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

## Reading in Obsidian

The `docs/` folder works as an Obsidian vault: graph view, backlinks and
quick switching come for free, and Mermaid diagrams render natively.

1. Open `docs/` as a vault ("Open folder as vault").
2. Settings → Files and links: turn off **Use [[Wikilinks]]** and set
   **New link format** to **Relative path to file**, so new links keep
   working on GitHub ([ADR-0002](adr/0002-docs-as-markdown-and-mermaid.md)).

`docs/.obsidian/` is git-ignored, so vault settings stay local.
