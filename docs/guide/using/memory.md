# Memory, sessions and reminders

## Memory

The agent keeps what it knows about you in plain Markdown files, one per topic, in `workspace/memory/`. The folder is a git repository, so every change has a history. The topics go into the agent's instructions every turn, within a size budget (`memory.prompt_budget`). When the budget is full, the topics changed longest ago fold to one line.

Memory is written in the conversation, from your own words. After a quiet spell in your private chat (10 minutes by default) or when a session ends, the agent also reads the chat once for facts it missed. You can turn that off with `memory.review.enabled: false`.

Every entry passes a check that rejects instructions disguised as facts. Suspicious entries are set aside and you are told.

`/memory` lists the topics. `/memory kitchen` shows one. It works in your private chat only.

### Remember, Skip, Undo

When the current conversation has read outside content (a web page, a forwarded message), facts the agent proposes are listed with two buttons, **Remember** and **Skip**. Nothing is saved until you press Remember.

After every change the bot sends a note listing it with an **Undo** button. Undo reverts that change.

## Sessions

A conversation is a session. The bot starts a new one after silence, 6 hours by default (`agent.session_idle`; `off` never does).

- `/new` archives the current session and starts a fresh one.
- `/sessions` lists the last 5 earlier sessions.
- `/resume 2` returns to session 2 from that list.

## Reminders and scheduled tasks

Ask in your own words:

```text
Remind me every Monday at 9 to water the plants.
Every morning at 7 check the news about Go and send me the three best links.
```

There are two kinds.

- A **reminder** sends fixed text at its time. No model runs.
- A **task** runs an instruction as a full unattended assistant turn, at its time.

A schedule is either a cron expression with five fields, or a single date and time in the node's time zone. Check `node.tz` first: the default is `Europe/Kyiv`, and it decides what "9 in the morning" means.

You confirm each one when it is created. For a task, the confirmation lists what it may do: its tools, the web hosts it may fetch, the exact host commands, and who gets the reply. You approve that once. After that, runs need no button presses, and anything outside the approved list is denied with one notice. Tasks count against your [daily limits](../running/models.md#limits).

`/tasks` lists them, one message each, with **Details**, **Delete** and **Run now**.
