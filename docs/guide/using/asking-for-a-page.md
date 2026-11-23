# Asking for a page

You can ask the agent for a small web page: a number you care about, a chart of the last day and the latest notices from your modules. The page lives on the computer where its module runs, usually an [edge](../running/operations.md#edges) next to your devices, and you open it in a browser on that same computer. Nobody else can reach it.

## Ask for one

Say what you want to see, not how to build it:

```text
Show me my battery on a page: the charge now, a chart of the last day,
and the latest alerts.
```

The agent builds the modules the page needs (often a reader that collects the values, and the page itself), links them and builds everything first. Then you get the usual install window with **Approve** and **Deny**: see [Reflex modules](modules.md#approve). For a page, the window also shows which computer the page runs on and its address. Nothing runs until you press.

## Open it

After you approve, the agent tells you the address. It has this form:

```text
http://127.0.0.1:8090/<name>/
```

`<name>` is the module's name. Open it in a browser on the computer that runs the module. The address list at `http://127.0.0.1:8090/` shows every page that computer serves.

A page shows:

- the current value, and how long ago it changed;
- a chart of the last day (a reading is kept every time the reader reports one, up to a day or a few hundred points);
- the latest notices, newest first, up to twenty.

The page asks for fresh data by itself every 15 seconds. It does not reload and you do not need to refresh it.

## What a page can and cannot do

- **Only on that computer.** The page listens on the computer's loopback address (`127.0.0.1`) and nothing else. It is not reachable from your phone, from another computer or from the internet. A request that names any other address is refused.
- **Only its own files.** The browser is told to load nothing from elsewhere: no outside scripts, fonts, images or trackers, and no page script that is not part of the module.
- **No sign-in and no cookies.** It does not need one, because only the computer itself can reach it.
- **Text from outside is shown as text.** A notice that contains a tag or a script is displayed as those characters and never runs.
- **The page only reads.** It cannot change a cell, send you a message or switch anything; to act, ask the agent in the chat.

## Change it

Ask for the change in the chat: "show the last six hours", "put the alerts first", "call it Battery". The agent rebuilds the module under a higher version and shows you the install window again; the old page keeps working until you approve.

If the page says "no data", the reader has not reported yet, or the module that feeds it is not linked: ask the agent, or look at `/modules` and `/cells` in the chat.

## Troubleshooting

- **The browser says it cannot connect.** The page runs on another computer than the one you are on, or the module is not running. `/modules` says where each module runs and whether it started.
- **"No answer; trying again" under the notices.** The module is not answering right now, or the link to its computer is down. The page tries again on its own.
- **The address is refused.** Use `127.0.0.1` or `localhost` with the same port; an address of the local network or a name of your own does not work on purpose.
