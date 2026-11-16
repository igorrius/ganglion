# Tools and safety

The agent can search the web, read pages and run commands on the machine. Everything is built so that you stay in control: a button press for anything that changes something, and a sandbox for anything that runs.

Only the admin's private chat gets tools. Group members get plain conversation.

## Web

- **Search.** Uses Brave if you set `agent.search.brave_key`, DuckDuckGo otherwise. Set `agent.search.mode: off` to give the agent no search.
- **Reading pages.** Only `http` and `https`, never private or local addresses, with size and time limits. A page you gave in your message, or one that came from search results, is read without asking. A page address the agent made up itself needs your approval.

## Host commands

The agent can run commands as the node's user, with no shell, inside a sandbox. Each command falls into one of three classes. The model cannot choose the class.

| Class | Examples | What happens |
|---|---|---|
| Runs by itself | `df`, `du`, `free`, `ls`, `ps`, `docker ps` | No button. |
| Needs a press | `cat`, `grep`, `rm`, `kill`, `docker rm`, any unknown program, `bash -c` | A card shows what it will do in plain words, with the agent's reason marked as the agent's own words. You press Approve or Deny. |
| Forbidden | `sudo`, `su`, `docker run`, `docker exec`, `crontab`, `systemctl --user start` | Never runs. |

For several steps, the agent posts a plan, and one **Run** button approves exactly the listed commands. Long commands continue in the background with a live card.

The sandbox keeps the command away from the node's own data, secret files, `.env` files and a list of common secret stores. Add more with `sandbox.deny`. It cannot stop `kill` from reaching your other processes.

If the machine cannot offer a sandbox (not Linux, running as root, a kernel without Landlock), host commands are off and `/status` says why.

The kill switch is `/commands off`. `/commands on` turns them back on.

## Confirmation buttons

By default, every action that changes something needs a button press. A decided question folds to one line such as "Approved, 21:09". A question that nobody answers expires.

A reminder is the exception: in a clean session it is set at once, and you see it with a **Delete** button; a [tainted session](#tainted-sessions) still asks.

If you change your request and the agent asks the same kind of question again (for example, a reminder for another time), the old question is replaced: its buttons go away and it folds to "Replaced by a newer question", so only the current one can be pressed.

## Tainted sessions

When the conversation reads something an outsider wrote (a web page, a search result, a forwarded message, a quote from another chat), the session is **tainted**. In a tainted session the agent might be reading instructions planted by someone else, so it becomes stricter. Any action that changes something, any message to someone else, any memory write and any script that takes input needs your press, even ones that would have run by themselves.

Send `/new` to clear it. It also clears by itself after 6 hours of silence.

## Secrets

The model never sees a secret. Secrets are redacted from logs and the tool-call journal before anything is written. Keep them in `env:` variables or the encrypted store, and never type one in chat. See [Configure](../getting-started/configure.md#secrets).
