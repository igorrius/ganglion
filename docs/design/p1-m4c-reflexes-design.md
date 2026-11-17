# M4c design: reflex modules

Design from the owner's brainstorm of 2026-11-10. It is the input for the
spec changes, ADRs [0065](../adr/0065-cell-triggers-through-the-inbox.md),
[0066](../adr/0066-notify-capability.md) and
[0067](../adr/0067-agent-writes-module-source.md), and the step-by-step plan
(`p1-m4c-reflexes.md`). It is not itself a plan.

Marks: **owner** means the owner decided it in the brainstorm; **call**
means the author's default, open to the owner's correction.

## 1. Intent

M4b ended with `deye-reader` running on cron without a model and writing
its cells. During the live check the agent proposed a scheduled task that
would wake the model every 30 minutes to compare the battery's charge with
thresholds and write to the owner. The owner rejected it: a routine
reaction that costs a model turn every time is exactly what ganglion
exists to avoid (owner).

The goal behind it (owner): the model builds and supervises chains of
small compiled modules — reader → rule → notifier — that run in the core
without a model. The model is called to build a chain, to fix it, and for
what only a model can do. Later a graph UI in the spirit of Node-RED shows
the chains and starts them (spec 9.6, far).

M4c takes the first step: one module reacts to another module's cells and
writes to the owner, and the agent writes that module itself.

## 2. Goals, non-goals

Goals:

- A module runs when cells it listens to change, with no model and no
  cron of its own.
- A module writes to the owner's private chat within a daily limit; the
  model sees only that it happened.
- The agent writes a module's source in the workspace, builds it, reads
  the compiler's errors and fixes them, and ends at the install window.
- The agent follows a skill on how to write modules; a reflex template
  gives it a working start.
- Live: the owner asks for battery alerts in one message; the agent
  writes, builds and offers `deye-alerts`; after the owner's press the
  alerts arrive with no model turn.

Non-goals (spec 14, "Not now"):

- Targets other than the owner's private chat: channels, groups, topics in
  a group (the family chat, topic "K7") (owner). They come with
  multi-channel delivery.
- A module asking a model (the cheapest one) to word a message or for a
  light analysis (owner). A separate security boundary: the module would
  write a prompt and spend the owner's limits. Designed when the first
  module needs it.
- The graph UI and flows (spec 9.6).
- NATS. The inbox is the transport now; it moves to NATS with the rest
  of the inbox (owner: "when NATS arrives we use it the same way").
- Deleting a source file with a tool; signed builds; a builder worker.

## 3. The five pieces

1. **Cell triggers** — a new trigger kind, delivered through the inbox
   (section 4).
2. **`notify`** — a capability to write to the owner (section 5).
3. **`module.write` and `module.read`** — the agent edits source in
   `workspace/modules/<name>/` (section 6).
4. **Template and skill** — `examples/modules/reflex-template` and the
   agent's module-writing skill (section 7).
5. **Acceptance** — live, by the owner, with `deye-alerts` written by the
   agent; in CI with a reference reflex (section 8).

## 4. Cell triggers

Decided: cell triggers, not a second cron (owner); delivered through the
node's inbox, not an in-memory subscription (owner).

### Manifest

```yaml
triggers:
  - cell: [deye-reader.soc, deye-reader.grid, deye-reader.sample]
  - cron: "15 * * * *"        # the template's staleness check
cells:
  read: [deye-reader.soc, deye-reader.grid, deye-reader.sample]
```

- Every cell a trigger names must be in `cells.read`, by full name. What a
  module listens to it can read, and the window shows it.
- A module may not list its own cells in a trigger (refused at build).
- The install refuses a trigger that would close a loop between installed
  modules (`a → b → a`), naming the loop (call).

### Delivery

1. A module's run (any trigger) writes cells through `cell_report`; the
   host collects the names it wrote.
2. When the run ends, for each installed, enabled module whose cell
   trigger names one of those cells, the host puts **one** event into the
   inbox: kind `module.trigger`, payload `{module, producer, run, cells,
   depth}`, idempotency key `trigger:<module>:<producer>:<run>` (call: one
   event per subscriber per producer run, however many cells it wrote).
3. The node routes `module.trigger` to the module host, not to the agent.
   The host runs the subscriber with `Input{Trigger: "cell", Payload:
   {"cells": [...], "producer": "deye-reader"}}`. The module reads the
   values itself with `cell_get`.
4. The event is acknowledged once the run ends, failed or not; a failure
   is the supervisor's business. A node that dies mid-run delivers the
   event again after the restart.

An event can be lost in one place only: the node dies after the producer's
run ended and before the put. The producer's next run triggers again, and
a reflex's own cron (section 7) covers a producer that went quiet.

### Limits and failures

- **Depth** (call). A run started by a trigger carries the depth of the
  chain; the events it causes carry depth + 1. Above 4 the host puts no
  event, writes the journal line "chain deeper than 4, stopped" on the
  producer and tells the owner once per active version, in the node's
  words. With loops refused at install, this is a backstop.
- **One run at a time** per module (R-RUN-12): events for a busy module
  wait in the inbox.
- **A module that is off, crash-looped or removed** gets no runs; its
  events are acknowledged and dropped.
- **Failures** are unattended runs, like cron: they count towards the
  crash loop, are journaled, and escalate once per kind until recovery
  (R-RUN-12).

### Visible

- Install window: "Reacts to: deye-reader.soc, deye-reader.grid,
  deye-reader.sample".
- `/modules deye-alerts`: "Reacts to: …"; `/modules deye-reader`: "Feeds:
  deye-alerts". The chain is visible from both ends, ready for a graph
  later.

## 5. `notify`

Decided: the owner's private chat only (owner); the full text to the
owner and only a node note to the model (owner).

### Manifest and host function

```yaml
capabilities:
  notify: {per_day: 24}
```

- Host function `notify(text) → code`. The message goes through the
  outbox (guaranteed delivery, retries) to the private chat of the admin
  who pressed the install: the same chat escalations go to.
- **Form.** The node prefixes the module's name: `deye-alerts: 🔋 70 %,
  discharging 1.2 kW, grid off`. Plain text: Markdown is escaped, no
  buttons, no link previews, at most 1000 characters, cut with "…" (call).
  The module cannot draw a button or pass for the node or the agent.
- **Limit.** `per_day` from the manifest, counted per calendar day in the
  node's time zone; the node's ceiling is 100 (call). Over the limit the
  message is not sent and `notify` returns the code "limit"; the owner
  gets one line from the node that day: "deye-alerts reached its limit of
  24 messages today".
- **Codes**: sent (queued), limit, not granted.
- **Exactly once** (call). Each message's outbox key is
  `notify:<module>:<run trigger>:<n>`: the triggering event's id or the
  cron slot, and the call's number in the run. A run repeated after a
  crash sends nothing twice.

### Model and history

- The owner's chat history gets a node note without the module's text:
  `[module] deye-alerts notified you at 14:30`. It does not taint the
  session. Asked "why did this alert come?", the model reads the cells or
  `module.status`.
- `notify` is a module capability, not a tool: scheduled tasks and the
  model cannot use it.

### Visible

- Install window: "Writes to you: up to 24 messages a day". A new version
  with a higher limit says so under "New:".
- `/modules <name>` and `module.status`: messages sent today and the
  limit; never their text in `module.status`.

## 6. The agent writes module source

Decided: no press per write (owner). The source does nothing by itself:
it is built in the confined sandbox and runs only after the owner's press
on the install window.

### Tools

- **`module.write {name, path, content}`** writes one file to
  `workspace/modules/<name>/<path>`:
  - `path` is relative, without `..`, hidden segments, `target/` or
    symlinks on the way; the folder is created for a new module, whose
    name passes the module-name rules (pattern, reserved names);
  - at most 256 KB a file, 16 MB and 200 files a module (call);
  - every write is a workspace commit "module `<name>`: write `<path>`";
  - no press; no delete in M4c (the template's files are rewritten, not
    removed).
- **`module.read {name, path?}`** returns a file of the source, or the
  list of files without `path`.
- The start of a new module is `module.import` of
  `examples/modules/reflex-template` under the new name, as in M4b.

### The security boundary

`module.write` is refused in a tainted session (R-POL-6, ADR-0049): text
from outside could otherwise dictate the module's code, and the install
window shows rights, not code. The refusal is in the node's words and
suggests starting from a clean session.

The agent's loop is write → `module.build` → read the errors → write. The
build's answer (the compiler's log, or the plan with the manifest's
rights) is outside content today, so the first failed build would taint
the session and stop the loop. The fix (owner approved the principle):

- The node keeps each module folder's **provenance**: the digest of the
  folder as the node's own tools last left it (`module.import` from the
  repository's examples, `module.write` from a clean session).
- When the folder's digest at build time equals that record, the build's
  answer is the agent's own work and **does not taint**. Only rustc's
  diagnostics about the module's own files are kept in that answer;
  lines from cargo and from dependencies' build scripts are dropped
  (call).
- Any other change to the folder (a command, the owner's editor) breaks
  the match, and the answer taints as today. `module.read` follows the
  same rule.
- The answer is clean only while the module's `Cargo.lock` lists, besides
  the module's own package, only crates.io crates (name, version and
  checksum) that an example's `Cargo.lock` lists too, since a dependency's
  build script or macro can write any text into the build's output (owner,
  2026-11-10). How the record and this rule were built is
  [ADR-0068](../adr/0068-vouch-for-module-source-per-file-and-vetted-crates.md):
  a digest per file, not per folder.

### What the agent sees

- `module.write`: `{written, commit}` or a refusal in the node's words.
- `module.build`: unchanged in form; its `external` flag follows the
  provenance rule.

## 7. Template and skill

### `examples/modules/reflex-template` (Rust)

- `manifest.yaml`: a cell trigger, an hourly cron for staleness,
  `cells.read`, `notify`, `state`, config keys with defaults.
- `src/logic.rs`: one pure function, `react(input, cells, state, now) →
  (state, messages)`. The agent changes only the manifest and
  `logic.rs`.
- Guest glue and a fake host, so `cargo test` runs the rules natively.
- The staleness check: when the producer stops, its cells stop changing
  and the cell trigger stays silent; the cron run compares each cell's
  `at` with a threshold and says "no data" once.

Dependencies are the template's `Cargo.lock`: the build runs `--locked`.

### The module-writing skill (in the agent's workspace)

Rules (owner, with the two mandatory instructions of spec 9.4):

- One module, one responsibility; data in and out only through cells with
  contracts.
- Message text from a template in code, not from a model. A model is
  asked only when a template truly cannot do the job, and the runtime has
  no such capability yet.
- Least rights: no network, secrets or `notify` the task does not need;
  `per_day` sized to the rules.
- A Rust test for every rule in `logic.rs`.
- The loop: import the template → write → build → read the errors → fix;
  end with `module.install` and the window.
- Never: listen to the module's own cells, write a cell another module
  owns, put outside text into a message.

## 8. Acceptance

### Live, by the owner

The owner writes to the agent in one message (owner):

> Send me battery alerts: while discharging at 90, 80, 70, 60, 50, 40, 30 %
> and every 5 % below 30 (25, 20, 15, 10, 5), with the charge, the
> discharge power and the grid; while charging, every reading with the
> charge and the power; once when the battery is full; once when the data
> has not updated for more than an hour.

Expected, with no model call per alert:

1. The agent imports the template, writes and builds `deye-alerts`, fixes
   its own errors, and ends its turn with the install window: "Reacts to:
   deye-reader.soc, deye-reader.grid, deye-reader.sample", "Writes to you:
   up to N messages a day", no network, no secrets.
2. After the press, the next `deye-reader` run (:00 or :30) writes its
   cells, `deye-alerts` runs, and a message arrives. The node's log shows
   no model turn for it.
3. `/modules`: both modules running, `deye-alerts` with runs in its
   journal; the chat history has the node note without the text.
4. The cards show the chain: `deye-alerts` reacts to `deye-reader`,
   `deye-reader` feeds `deye-alerts`.

### In CI, without a model

- A reference reflex in `testdata` (not `deye-alerts`, which the agent
  writes).
- chatlab scenarios: a cell trigger → `notify` → a message; the daily
  limit; a loop refused at install and a chain stopped at depth 4;
  `module.write` refused in a tainted session; a build of a clean folder
  that does not taint and one of a changed folder that does; an event
  that survives a restart; a repeated run that sends nothing twice.

## 9. Documents before code

- ADR-0065: cell triggers through the inbox (options: inbox, in-memory
  watch, a separate bus).
- ADR-0066: the `notify` capability (options: the owner's chat only,
  targets in config, both).
- ADR-0067: the agent writes module source in M4c, with the provenance
  rule (options: a tool without a press, a press per write, a press per
  module); it moves "the model writes the source" from P3 (ADR-0061, spec
  9.4) into M4c and keeps signatures and the worker in P3.
- Spec: R-RUN-15 (cell triggers), R-RUN-16 (`notify`), R-RUN-17 (module
  source written by the agent); the ABI table and the example manifest in
  7.5; 9.4 and 10.2; "Not now" gets targets beyond the private chat and a
  module asking a model.
- The architecture views follow the code in the plan's tasks.

## 10. Background jobs with a live card (live check, 2026-11-10)

The first live run stopped at the build. The agent read the skill and the
cells, imported the template as `battery-alerts`, wrote `manifest.yaml` and
`src/logic.rs` from a clean session, and called `module.build`. With the
`claude` CLI route one model call runs every tool call of the turn, and
that call is limited to 60 s (the turn to 2 min, the node's handler to
3 min). Writing the code took about 40 s, so the build was stopped after
10 s, reported as "ran past 10m0s" (its own limit, which it never reached),
and the turn failed. A build that fetches crates and links with LTO takes
a minute or more; a turn is the wrong place for it.

The owner also named the general pain (owner): agents give no sign of
life. While something runs, the person cannot tell whether it is alive,
what it is doing or when it will end.

Decided (owner): builds run in the background, with a live card; the chat
stays usable while they run; the card is one mechanism for every
background job of the node, `host.exec` included.

- **A background job** is work the node runs outside any turn: a build
  (always), a host command that outlives its 20 s wait (as today). The
  node keeps its row (kind, key, conversation, title, started, state,
  progress) so a restart can finish it honestly.
- **The card** is one message the node sends when the job starts and
  edits in place on a timer, without a model: `🔨 battery-alerts · build ·
  0:45 · crates built: 14`, every 10 s (call). The ticking time is the
  sign of life; a frozen time means the node stopped. It ends as `✅ built
  in 1:32`, `❌ build failed in 0:40`, or `⚠️ interrupted by a restart`.
  Its words are the node's fixed texts; it shows no text from the build.
- **The result comes back at once** as an event that starts a turn, not
  after a 30 s poll. For a build the turn gets a note with the build's
  answer, clean or outside content by the provenance rule (section 6),
  and the agent goes on: it fixes and builds again, or offers the window.
  The note resumes the agent at the build call's own depth, since the build
  is the node's work; instead at most 5 builds of a module run in a row in
  a conversation before a person writes there again (owner, 2026-11-10).
  Each build's answer and its outside-content flag are kept in a record
  file under `<data>/modules/build-jobs/`, out of the command sandbox's
  reach, which the resumed turn reads.
- **The model knows what runs.** Every turn of the conversation carries a
  node note listing its running jobs (`[jobs] build battery-alerts 0:45`);
  it is not stored in history. Asked "how is it going?", the agent answers
  from it.
- **`/status`** shows a "Background jobs" section with the same lines.
- **The chat stays alive** (owner). New messages start ordinary turns.
  Only work on the same module waits: `module.build` and `module.write`
  of a module whose build is running or queued are refused in the node's
  words ("battery-alerts is being built; wait for its result").
- **One build at a time** per node (call): a build is heavy on a small
  host; the next waits in a queue and its card says "queued".
- **The model call and the turn get more time** (call): a call 3 min, a
  turn 5 min, the node's handler 6 min (under the 10 min lease), set in
  `ganglion.yaml`. Writing a module's code is a long answer, and the CLI
  route runs a turn's tools inside one call.
- **The builder's message** says what stopped it: its own limit, or the
  caller's deadline.

Records: [ADR-0069](../adr/0069-background-jobs-with-a-live-card.md); spec
R-LIVE-9, R-CMD-4, R-RUN-9; plan tasks 9 and 10.
