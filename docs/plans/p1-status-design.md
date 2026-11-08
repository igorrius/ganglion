# `/status` in Telegram: design

Status: approved by the owner on 2026-11-08. It feeds the spec changes and the step-by-step plan.
It is not itself a plan.

Marks, as in the [M3d design](p1-m3d-host-commands-design.md): **(owner)** the owner decided it;
**(call)** the author's call, open to correction; **(builder)** left to whoever implements it.

## 1. Why

The owner has no one place that answers "is everything all right with the bot?". `/last` shows one
turn, `/metrics` a picture of numbers; what is off and why (a skill not loaded, a module fallen back,
Telegram's `409`) sits only in the logs. M3d adds one more such fact ("commands are off: <reason>",
[M3d design](p1-m3d-host-commands-design.md) section 5), and the network of P2 adds nodes and NATS.

**What success looks like:** one command, one screen, no model: a verdict first, then the node, the
models, delivery, and what is off, each line with its reason. A new component shows up in it by
implementing one interface.

## 2. Goals, non-goals, anti-goals

**Goals:**

1. `/status` answers how the node is doing now, for an admin, without a model **(owner)**.
2. Sections: the node and its memory, the models and spend, delivery, what is off and errors
   **(owner)**.
3. The version of ganglion, in full **(owner)**.
4. The model the current session uses, so a later choice of model in the chat shows here with no
   change to `/status` **(owner)**.
5. Modular: every component contributes its own lines through one entity, so NATS (P2), Deye (M4)
   and the sandbox (M3d) add theirs without touching `/status` **(owner)**.

**Non-goals:**

- Checking the configuration for likely mistakes: that is a terminal command, `ganglion doctor`, and
  goes to "Not now" **(owner)**. `/status` assumes the node runs and says how.
- Choosing the model in the chat: "Not now" **(owner)**.
- A tool for the model to read the status **(call)**: nothing asks for it yet.
- Alerts when a line turns red **(call)**: the existing alerts stay as they are.

**Anti-goals:**

- No raw error text in the message: a Telegram description or a model API's error can carry outside
  text **(call)**. The logs and `ganglion mcp` keep the detail.
- No probe calls to models: they cost money and time **(call)**.

## 3. What the admin sees

The reply is a fixed text in the person's language
([ADR-0053](../adr/0053-fixed-texts-in-the-persons-language.md)); names of models, modules and skills
and the numbers go in as values. The first line is the verdict, counted from `warn` and `fail` lines
only:

- ✅ all well;
- ⚠️ needs a look: N (some `warn`, no `fail`);
- ❌ not working: N (any `fail`).

Then four sections, each under a heading, so the reply goes out as a rich message
([ADR-0054](../adr/0054-structured-replies-as-rich-blocks-the-transport-builds.md)) and the ordinary way
when Telegram refuses it. A line is a state mark, a name, a value, and for anything not `ok` a reason
and the time since. An example, in Russian:

```
⚠️ Требует внимания: 1

Нода
✅ ganglion v0.1.0, коммит 4ab9e56 от 2026-11-07, Go 1.27.3 linux/arm64
✅ Работает 3 д 4 ч
✅ Память 45 MB, пик 99 MB; GOMEMLIMIT не задан
✅ База 18 MB, логи 6 MB

Модели
✅ В этой сессии: claude-sonnet, запасная claude-haiku
✅ claude-sonnet: отвечала 12:40
✅ claude-haiku: отвечала 12:38
✅ Сегодня у вас: 41 ход из 200, 1,2 млн токенов из 3 млн; расход $0,84

Доставка
✅ Telegram: опрос 12:41
✅ Входящие: 0 ждут; исходящие: 0 ждут

Выключено и ошибки
⚠️ Скилл rates не загружен: скрипт не исполняемый (с 09:15)
✅ Неудачных ходов за сутки: 0
✅ Задачи: 3, следующая 18:00, отставание 0 с
```

- The version line is what `ganglion version` prints: version, commit, commit time, Go, OS and
  architecture; "(modified)" for a build from a changed checkout; "dev, commit unknown" without VCS
  information **(owner)**.
- The session line is the router's candidates for an ordinary turn in this conversation now, after
  its hard filters (`privacy: local-only` among them). When the session's last turn was answered by a
  fallback, the line says so **(owner, call for the wording)**.
- `off` lines (switched off by configuration, such as `web.search` without a key) are shown and do not
  count towards the verdict **(call)**.
- Someone who is not an admin gets the same answer as for `/last` **(call)**. The command is not a turn
  and leaves no note in the history, as `/last` **(call)**.

## 4. The entity and its sources

A new package `status` holds the entity and the collector, nothing else:

- `Check`: `Section` (node, models, delivery, problems, in this order), `Name`, `State` (`ok`, `warn`,
  `off`, `fail`), `Value`, `Reason` and `Since`.
- `Reason` is a key of the `texts` catalogue with parameters: numbers, times, and names from the
  configuration. It is never an error's text (section 2, anti-goals).
- `Source`: `Status(ctx, Scope) []Check`. `Scope` is the conversation and the person who asked; only
  the session's model line uses it.
- `Collect(ctx, scope, sources)` asks every source at once, each under its own timeout of about 2 s
  **(call)**. A source that times out, fails or panics becomes one `warn` line, "<component>: no
  answer", and the others are shown as usual. `Collect` never fails.

Sources, each owned by the component that knows the fact:

| Section | Source | Lines |
|---|---|---|
| Node | node (`buildinfo`, `/proc/self/status`, `runtime/debug`) | version; uptime; RSS and peak; `GOMEMLIMIT` when set; the database and log sizes |
| Models | router and `turns` | the session's candidates; per model in the catalogue, its last answer and last failure; the asking admin's day against the daily limits, and the day's spend |
| Delivery | Telegram transport | the last successful poll; a `409` that stopped polling |
| Delivery | `queue/sqlqueue` | inbox and outbox waiting, the oldest waiting, undelivered in the last day |
| Problems | `skills` | each skill not loaded, and why |
| Problems | `modstore` | each module fallen back to its built-in, and why |
| Problems | `turns` | failed turns in the last day |
| Problems | `schedule` | jobs, the next run, the lag |

A fact that is a moment, such as a `409` or a skill refused at start, is kept in memory by the
component that saw it and given out when asked; nothing is stored for `/status`.

`config` registers each source in `samber/do`, and the agent gets them as a list
([ADR-0028](../adr/0028-samber-do-composition-root-in-config.md)). A new component implements `Source`
and adds one registration line. In the network of P2 an edge's lines can ride the bus in the same
type (R-OBS-9); that is P2's work.

The agent renders the lines, as it renders `/last`: the verdict and marks from `State`, the texts from
the catalogue in the person's language.

## 5. Thresholds

Constants in code, not settings **(call)**:

| What | `warn` | `fail` |
|---|---|---|
| Memory | RSS above 90% of `GOMEMLIMIT` (no line without a limit) | — |
| A model | its last attempt failed, with no answer since | every model in the catalogue so |
| Daily limits | 80% of the turns or tokens limit | the limit reached |
| Telegram | no successful poll for 2 minutes | polling stopped by `409` |
| Queues | the oldest waiting for over a minute; anything undelivered in the last day | — |
| Skills, modules | a skill not loaded; a module fallen back | — |
| Turns | any failed turn in the last day | — |
| Scheduler | a lag over a minute | — |

Telegram counts polls, not messages: a quiet chat is normal.

## 6. Security

- An admin's command only; others learn nothing from it.
- No error's text reaches the message: reasons are catalogue keys with parameters from closed sets,
  the configuration, numbers and times. Names of skills and modules come from the node's own files.
- No new boundary: the sources read what the node already holds. The reply goes through the outbox
  as any reply, with the session's origin (R-DLV-14).

## 7. Errors

- A source that does not answer in time, fails or panics: one `warn` line, the rest shown.
- The rich message refused: the ordinary message (ADR-0054).
- A text missing in the person's language: the catalogue's per-key fallback (ADR-0053).

## 8. Checks

- Unit tests per source, against fakes or a temp database.
- `Collect`: order of sections, a slow source, a failing one, a panicking one; never an error.
- The command: verdict for each mix of states, the session line with and without a fallback, a
  non-admin, the person's language, no history note.
- Chatlab: a mechanical scenario on the scripted model: `/status` answers with the four sections, and a
  seeded fault (the transport reports a `409`) turns the verdict red.
- The owner's look on the test bot.

## 9. Docs to change with it

- Spec: R-OBS-11 for `/status`; "Not now": `ganglion doctor`, choosing the model in the chat.
- Phase plan: task 3.38 points here and to the plan.
- Code map: the `status` package and the sources in their packages' rows.
- `docs/README.md`: "Where we are" when it closes.
- No ADR: no dependency, format, runtime or boundary is added **(call)**.
