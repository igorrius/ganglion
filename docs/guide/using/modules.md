# Reflex modules

A **reflex module** is a small program the agent writes for a routine task. It is compiled to WebAssembly and runs on a schedule, or when certain values change, without calling a model. It is fast and costs nothing per run.

A **cell** is a named value owned by a module, such as `battery.level`. A cell holds a sensor reading or a switch. A module writes only its own cells and reads another's only if it declared so. `/cells` shows all of them.

Modules need a kernel with Landlock (6.2+) and Rust on the node. See [Requirements](../getting-started/requirements.md).

## Ask for one

Describe the routine task to the bot in your private chat:

```text
Every 15 minutes read the price from https://example.com/api/price
and tell me when it falls below 100.
```

The agent chooses a template, copies it under a new name, edits the files that matter and builds it in the background (a live card shows progress; when it rebuilds a module, the same card shows the new build and its number). It asks you nothing on the way: copying a template, writing the source and building never ask, and it fixes and rebuilds on its own until the module's examples pass. While it builds it does not write to you either: the card shows the progress. Then it offers to install what it built, once. If you write to it while it builds, it answers.

There are two templates:

- **reader**: reads an outside HTTP service on a schedule and reports the reading to a cell. It handles rate limits (waits and retries), marks a reading as stale when it gets too old, and stops retrying when a login fails.
- **reflex**: listens to cells and tells you when a rule fires, for example a value crossing a threshold downwards, or a value that has not changed for too long. It says each thing once.

Describe what you want to get, in your own words; you do not need to know how it is built. The agent decides how many modules the task takes and how they pass data to each other. Often it is both: a reader feeds the cells, a reflex watches them. When each occurrence matters (every new order, every alarm), the agent passes them as events over a link between the modules instead. Either way it builds everything first and then offers the modules together, with the links between them.

## Approve

Nothing runs until you press. When the agent has built everything you asked for, you get one install window with **Approve** and **Deny** for the whole request: one to five modules and the links between them. For each module it lists the network hosts, secrets, cells, triggers, schedule, message limit and the ports (the events and calls the module sends, takes, answers and makes). A new version that asks for more rights than the last says so on its own line. If the agent wrote the module's source after it had read outside content (a web page, a forwarded message), the window says so on that module's block: read what that module may do with extra care. After the modules the window lists the links, each as the two ports with the shape that crosses it. The links are checked as if the modules were already installed, so a link may join two modules of the same request; if the node would refuse a module or a link, there is no window and the agent is told why. One press installs the modules in the order shown and writes the links. If one step fails, the rest is not tried, and what was already installed stays; the agent tells you what was done and what was not. If the module ships examples, the window also shows what its trial run sent and wrote, so you see its messages and values before you approve. Read it.

### The trial run

A module comes with an `examples.yaml` beside its `manifest.yaml`: a short list of situations (a value arrives, time passes, a trigger fires) and what the module must do in each (how many messages it sends, which cells it reports, whether it fails). Right after a build, your node runs the built module on these examples in a sandbox. Nothing of your node changes: no real message goes out, no cell is written, and a module that uses the network gets the answers written in the file instead of reaching the service; a request to an address the example does not answer fails that step. A step can also deliver an event or a call to the module's ports, and count the events the module sends, so a module that only reacts to events is tried too. The agent writes the examples for each module it builds, one per behaviour you asked for.

The install window then has a "Trial run" section: how many examples passed, and for each the messages the module would have sent, the events it would have emitted and the cells it would have reported. For a module that listens to cells and needs no network, a last run on the cells as they were when the module was built, with your settings, shows what it would do with them.

A failed trial blocks the install: the build is refused, no window opens, and the agent is told which example and step failed and why, so it can fix the module or the example and build again. A module with no `examples.yaml` can still be installed; the window says no trial was run.

Modules cannot be installed from a ready-made file, a link or a chat message. They are built from source on your node.

If a module needs a secret (for example an API key), the agent asks for it and the node sends you a card to enter it from your phone: see [Entering a secret](secrets.md). Or set it on the host with `ganglion secret set --module <name> <NAME>`. Never paste the value itself into the chat.

## Versions and rollback

Every build is a new version, with a record of who built it and why. A build must carry a higher version than any earlier one. The node keeps the previous version. If a new version does not start, it is rolled back. Module state survives a swap.

## The `/modules` verbs

All of these work in your private chat and none restarts the node.

| Command | What it does |
|---|---|
| `/modules` | Lists modules. |
| `/modules <name>` | Shows a card: state, version, health, runs, last failure, settings, version history. |
| `/modules update <name>` | Updates a built-in module, one that ships inside the program, to the newest release. The node checks the release's checksum and permissions and runs a self-test first. If a check fails, the current version stays. It does not update modules the agent built: for those the bot answers that there is no such module. Ask the agent for a new version instead. |
| `/modules rollback <name>` | Goes back to the previous version. |
| `/modules off <name>` and `/modules on <name>` | Pauses and resumes a module. |
| `/modules remove <name>` | Removes a module. Its cells and history stay, read-only. |
| `/modules config <name> key=value` | Changes a setting the module declares. |
| `/modules runs <name>` | Lists the module's last runs, newest first. See [Why did a module write?](#why-did-a-module-write). |

## Why did a module write?

The node keeps a journal of every run of a module: when it ran, what started it (the schedule, the agent's call of one of its tools, a change in a value another module wrote, or an event or a call that reached it over a link, with the link named), how long it took, how it ended, which kinds of calls it made (network requests, reading and writing values, messages), the names of the values it wrote and how many messages it sent. It never keeps what the module said, the values themselves or the arguments of a call.

- **Reply to a module's message with `/why`.** The bot answers with the chain that led to it, oldest first: for example a reader that ran on its schedule and wrote a value, then an alert module that ran because that value changed and wrote to you. A run that a link started shows the link, for example "by an event over `reader.reading → bridge.in`". A module started by its schedule or by the agent has a chain of one run: its own. It works on the original message only: a forwarded copy carries no link to the run, so reply to the message in the chat where the module wrote it.
- **Press Runs on a module's card** (`/modules <name>`) or send `/modules runs <name>` to see its last runs, newest first. A run started by another module's value says which module, and one started over a link says which link.
- **Ask the agent.** `module.status` shows the agent the last 5 runs, and when you reply to a module's message and ask the agent about it, the agent is told how the message came about.

The journal keeps the last 200 runs of each module. A message sent before the journal existed, or a run older than the last 200, has no chain: the bot says so. Removing a module drops its runs. Names that do not look like a module or a value name are shown as `?`.

## Links

A module can declare ports: events it publishes, events it takes, and calls it answers or makes. A link connects the output of one module to the input of another, so a module can be wired to another without being rebuilt. The links are the `links:` list in `ganglion.yaml`:

```yaml
links:
  - { from: battery-reader.reading, to: battery-alerts.reading }
```

Each end is a module's short name, a dot and a port name. A module that may message you also has an output called `notify`, so a notice can be wired like any event.

You do not write links by hand. Ask the agent to connect two modules. It shows a card with both ends and the shape of what would cross the link, and writes the link when you press Approve. Nothing is rebuilt. The agent can also list the links and remove one, each with a card. A link is accepted only when both modules are installed, the ends are an output and an input of the same kind (an event to an event input, a call to a call it can serve), their shapes are equal, the links and the modules that react to each other's values form no loop, and no chain of modules is deeper than 4. Otherwise the agent tells you why, and no card appears.

The same checks guard the modules. A new version of a module that drops a linked port, or changes its kind or its shape, is refused, and the install window names the links it would break. Removing a module also removes its links, and the reply names them. If a link stops working anyway, for example because you edited the file by hand, the node logs it once at start and `/status` lists it under Problems with the reason; such a link carries nothing.

Events flow over a link once it is written. A module publishes an event on one of its outputs, and the node delivers it to every module linked to that output: each receiver runs once with the event. If the run fails, the node delivers the event again, after a pause that grows from one second to five minutes, until a run succeeds. A module you turn off does not lose its events: they wait for up to 7 days and are delivered when you turn it on again. A new link carries only the events sent after it was made, and removing a link drops the events it had not delivered. Delivery is at least once, so a module that emitted before it failed may emit the same event again on the retry. An event that does not fit the receiving port, or comes from a chain of more than 4 modules, is dropped and logged.

Calls flow over a link once it is written. A module that declares a calling port can ask the module its link names and wait for the answer, at most the time left in its own run. A module that serves a port runs for each request, and its result is the answer. Nothing is stored or retried: if the serving module is off or not running, the call fails at once, and the caller decides what that means. A calling port has one link: a second link from it is refused, and in `/status` it is listed as not working. A run that serves a call and fails counts toward the crash-loop rule below, but it does not message you on its own: the caller sees the failure. A request that does not fit the port, or comes from a chain of more than 4 modules, is refused and logged.

## The crash-loop rule

If a module fails 5 times within an hour, the node turns it off, raises an alert, and `/status` explains. Fix the cause (or ask the agent to), then turn it on again with `/modules on <name>`.

## Notification limits

A module may send you plain-text messages only if it was granted that, with a limit of up to 100 a day (counted in the node's time zone). Past the limit nothing is sent and you get one note. Each message is signed with the module's name and version, has no buttons, and is cut at 1000 characters. If you reply to one, the agent knows which module wrote it.

Other limits: one build at a time per node, and the agent builds a module at most 5 times in a row before you write something in that chat again.
