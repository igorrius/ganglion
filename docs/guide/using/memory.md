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

A reminder is set at once and shown back to you as a message with a **Delete** button, so a mistake is one press away. If the session is [tainted](tools-and-safety.md#tainted-sessions), the agent asks first, as it does for a task. You confirm a task when it is created. The confirmation lists what it may do: its tools, the web hosts it may fetch, the exact host commands, and who gets the reply. You approve that once. After that, runs need no button presses, and anything outside the approved list is denied with one notice. Tasks count against your [daily limits](../running/models.md#limits).

`/tasks` lists them, one message each, with **Details**, **Delete** and **Run now**. When you ask the agent in words to show your reminders, you get the same messages; when it only looks one up for itself, nothing is posted to the chat.

### Stopping a reminder

A repeating reminder arrives with a **✅ Done** button. Press it and the reminder is deleted at once: the message stays, with a line saying the reminder is off. No model runs. A one-time reminder has no button, since it is over once sent. The button works for a week after the message.

You can also answer in words. Reply to the reminder's message, for example "ordered" or "done, stop reminding", and the agent knows which reminder you mean and deletes it. You get one short line back, such as "🗑 Off: Order a flashlight.", and no question. The same goes for "stop the morning news task" said in plain chat.

If the session is [tainted](tools-and-safety.md#tainted-sessions), the agent asks you to confirm the delete first, so a web page or a forwarded message cannot stop your jobs. System jobs, such as the nightly memory audit, and the jobs of modules cannot be deleted this way at all.
