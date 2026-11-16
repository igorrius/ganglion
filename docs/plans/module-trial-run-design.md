# Trial run of a module before install — design

Issue: [#167](https://github.com/igorrius/ganglion/issues/167). Decision:
[ADR-0090](../adr/0090-a-module-is-tried-on-its-examples-before-install.md);
spec R-RUN-19. Agreed with the owner on 2026-11-16.

## 1. What and why

After `module.build` there is no way to see that a module does what it was
asked: the build runs `cargo build`, `Host.Check` runs only the `health`
trigger, and the install window shows rights, not behaviour. The owner
wants to see, before pressing Install, what the module would write and
send on concrete inputs, and the agent wants to know whether its module
works without reading the module's text (R-RUN-13).

A **trial run** runs the real `.wasm` on the module's own examples, in a
sandbox where nothing of the node changes, right after the build.

## 2. Decisions taken with the owner

| Question | Answer |
|---|---|
| Where the examples live | `examples.yaml` beside `manifest.yaml`, written by the agent like the other source files |
| Modules that use the network | Steps carry canned HTTP answers; any other request is refused; secrets are fake |
| Expectations | Yes, counted: messages, cells reported, failure; the node compares and words the result |
| A failed trial | Blocks: the plan is refused and the install window does not open |
| No `examples.yaml` | Allowed; the window says no trial was run |
| A run on the current cells | Yes, shown only, for a module with a cell trigger and no network |

## 3. The examples file

```yaml
examples:
  - name: charging crosses 60%
    steps:
      - cells: {inverter.soc: 55}
        trigger: cell
        expect: {messages: 0}
      - after: 10m
        cells: {inverter.soc: 67}
        trigger: cell
        expect: {messages: 1}
  - name: reader parses the answer
    secrets: {token: fake}
    http:
      - url: https://api.example.com/v1/state
        status: 200
        body: '{"soc": 55}'
    steps:
      - trigger: cron
        expect: {reports: [demo-reader.soc]}
  - name: tool answers
    steps:
      - trigger: {tool: battery.status, args: {}}
```

- An example starts from an empty state, no cells, virtual time at the
  trial's start (or at its own `start:`, an RFC 3339 time, so canned data
  with timestamps can be judged against a fixed time), and the manifest's
  config defaults; `config:` may set other values the manifest declares.
- A step, in order: `after` (a Go duration, moves virtual time on, ≥ 0),
  `cells` (values set, each stamped with the virtual time), then
  `trigger`: `cell` (the step's cells that the manifest's cell trigger
  lists are the changed ones), `cron` (the manifest must have a cron
  trigger), or `{tool: <name>, args: <object>}` (a tool trigger of the
  manifest). A step without a trigger only sets cells and time.
- `expect`, all optional: `messages: N` (exact count of notify calls the
  port accepted in this step), `reports: [ids]` (the exact set of cells
  the step reported), `fails: true` (the step must end in an error).
  Without `fails`, a step that ends in an error fails the example.
- `http` (per example): answers by exact URL (`status`, `body`, optional
  `headers`); each URL's host must be in the manifest's `http.fetch`.
  `secrets` (per example): fake values for names in `secret.get`.
- Checked by the node before anything runs, so a bad file is a refusal
  with the reason: the YAML is strict (unknown fields refused); cells in
  `cells` are in `cells.read` or the module's own; triggers exist in the
  manifest; at most 10 examples, 20 steps each, 32 KiB for the file,
  64 KiB per HTTP body.

## 4. The sandbox

A new package `modtrial` runs one build on its examples:

- `runner.Load` with the manifest's limits and `Deps`:
  - `State`: `runner.NewMemState()` per example;
  - `Cells`: an in-memory port seeded from the steps; `cell_report` is
    checked against the manifest's `Shape` as on a node, recorded in the
    report and visible to later `cell_get` calls of the same example;
  - `Notify`: a recording port that applies `per_day` per virtual day
    and answers the codes a live port does (a refused notify changes a
    reflex's behaviour);
  - `HTTP`: a client whose transport answers only the example's canned
    URLs; anything else is a transport error;
  - `Secrets`: the example's fake values only;
  - `Clock`: the example's virtual clock (new in `runner`, see below);
  - no telemetry, no failure counting, no escalation, no real cells, no
    listeners. `modhost`'s `call`/`finish`/`settle` are not used.
- **Virtual time.** `runner.Deps.Clock func() time.Time` (nil = the real
  clock) feeds `now_ms` and wazero's wall and monotonic clocks, so a
  module's staleness checks and anything it times see the example's time.
- **Budget.** Each call has the manifest's timeout; the whole trial has
  60 s; past it the remaining steps are reported as not run and the trial
  fails.
- **The run on the current cells.** For a module with a cell trigger and
  no `http.fetch`, one more run: its `cells.read` seeded from the node's
  real cells (read, never written), the cell trigger fired with them as
  changed, an empty state. Shown to the owner, no expectations, a crash
  there does not fail the trial (the real cells may be empty).

## 5. Where it runs and what is kept

- Inside the background job of `module.build`, after the builder returns
  the artifact and before the plan: the job reads `examples.yaml` from the
  module's source folder, runs the trial, and stores the report beside
  the build as `<Builds>/<sha>.trial.json` (swept with the build).
- A failed trial (a step's expectation not met, a step failed without
  `fails`, the budget spent, the file refused) makes the build's plan
  refused: `Plan.Refusal` "the trial run failed" with the node's words as
  the detail. `module.install` then refuses, as for any refusal.
- The report holds, per example and step: the outcome, the counts, the
  kind of a failure (`module.trap`, `module.timeout`, …), the reported
  cells with their values, and the message texts. Texts are kept only to
  show the owner.

## 6. What each side sees

- **The model** (in the build's answer, `trial` field): node words only,
  no module text:
  `trial: 3 examples, 2 passed; "charging crosses 60%" step 2: expected 1
  message, got 0` or `"tool answers" step 1: failed (module.trap)`, or
  `no examples.yaml: no trial run`. Example names come from the source,
  so they are as outside as the rest of the answer (clean only for the
  node's own source, R-RUN-17).
- **The owner**, in the install window, a section after the rights:
  "Trial run: 3 examples, all passed" then per example its name and, per
  step that produced something, the messages (escaped, one line each, as a
  module's message is shown) and the reported cells as `id = value`; then
  "Now:" for the run on the current cells. The section is cut at 1200
  characters with "… and N more" so the whole question stays within
  `tool.MaxQuestionRunes`. It is built from the stored report, so the
  window is the same at the question and at the press (`sameWindow`).
- No `examples.yaml`: one line "No trial run: the module has no examples."

## 7. Templates, skill, texts

- `examples/modules/reflex-template/examples.yaml` and
  `reader-template/examples.yaml` with examples that pass; the template
  tests build them and run the trial.
- The built-in skill `module-writing`: write `examples.yaml` with each
  module; one example per behaviour the owner asked for, with counted
  expectations; read the trial line in the build's answer and fix the
  module or the examples.
- Texts in en, uk, ru: the window's section, its lines and the
  refusal.

## 8. Tests

- `modtrial`: parsing and every refusal of the file; cell seeding and
  `at_ms` from virtual time; `after` and a staleness decision; `per_day`
  per virtual day; canned HTTP and a refused URL; fake secrets; the budget;
  expectations met and not met — on the `probe` test module
  (`runner/runnertest/probe`), scripted by config.
- `runner`: `Clock` feeds `now_ms` and WASI time.
- `tool/builtin`: the build's answer carries the trial line; a failed
  trial refuses; the window shows the section and stays equal between ask
  and press; no examples gives the "no trial" line.
- Lab: a scenario that builds the reflex template with examples and checks
  the window's trial section; one with a failing expectation that checks
  the refusal and that the model's text names the step, not the message.

## 9. Not now

- `cargo test` during the build.
- A "Try again" button or a trial on demand outside a build.
- Expectations on message text (the model would have to write it, and
  the node would compare text it never shows the model).
