# M3d design: host commands and plans

Design from the owner's brainstorm of 2026-11-03. It is the input for the
spec changes, the ADRs and the step-by-step plan (`p1-m3d-*.md`). It is
not itself a plan.

Marks: **owner** means the owner decided it in the brainstorm; **call**
means the author's default, open to the owner's correction.

## 1. Intent

The owner's disk filled to 100 %. Another assistant (OpenClaw, on the same
machine) cleaned it up from Telegram: it measured the caches, ran
`go clean -cache` and `docker builder prune`, removed images on request,
and built a tree of what takes the space. Nothing had to be typed in a
console. The owner sees this as an everyday need of real users, both for
an agent on their own machine and for one in the cloud, and as something
that should also run unattended on a schedule. ganglion has no tool for
it today.

The same session showed what must not happen. That assistant runs every
command as the user with no approval at all. Two moments matter:

- to remove root-owned leftovers of the Go cache it tried
  `sudo -n rm -rf …` on its own; it failed only because this machine has
  no password-less sudo;
- asked to "remove the images too", it ran `docker rm $(docker ps -aq)`
  and deleted 19 stopped containers to free them, and said so afterwards.
  A stopped container can hold data that exists nowhere else.

The owner also liked one thing there: before the work it posted a plan
with checkboxes in Telegram and ticked them as it went. The agent plans
first instead of charging in, so it does nothing extra.

So M3d gives the agent the commands an ordinary Linux user has, with the
safety in code, not in the prompt, and a plan message that doubles as the
approval.

## 2. Goals, non-goals, anti-goals

**Goals**

- A command tool: the agent runs programs as the node's Unix user, on the
  node's own machine (owner).
- Rules in code decide which commands run by themselves, which need the
  owner's press, and which never run (owner: "it must be code, not only
  an instruction to the model").
- A plan message: the agent posts the steps before the work and ticks them
  as it goes; one press on the plan approves every step that needs it
  (owner).
- Scheduled tasks may run approved commands unattended, e.g. prune Docker
  images every week (owner).
- On a cloud node, at least the basic picture: free and used memory, the
  agent's own memory, disk space (owner).

**Non-goals**

- Root. `sudo`, `su`, `doas`, `pkexec` never run, with or without a press
  (owner).
- Managing the cloud itself (Railway services, volumes, deploys): through
  that cloud's MCP server, which needs ganglion as an MCP client. That is
  P3 in the [M3b and M3c design](p1-m3b-m3c-design.md#2-goals-non-goals-anti-goals)
  (owner, 2026-11-03: "for that there is MCP").
- Commands on other machines (over `ssh`, or on another node): P2, with
  tool placement (R-RTR-3) and the distributed tool pool (spec 9.5).
- An interactive shell or a terminal (TTY).
- Linux only (call). On another OS the command tool is off; the basic
  stats (section 6) still work.
- The plan grant covers host commands only; other tools keep their own
  confirmations (call). Plans that approve other tools' calls go to
  "Not now".

**Anti-goals**

- A deny-list of strings presented as security. `bash -c`, `$(…)`,
  `python -c "shutil.rmtree(…)"` and `find … -delete` walk around any
  such list.
- A command that can read the node's secrets: the bot token, provider
  keys, the database, the node's own environment.
- A press that silently covers more than it showed: a step outside the
  approved plan, or a scheduled run doing anything not shown when the
  task was created.
- The agent widening its own rights: it cannot edit the command rules.
- An agent that leaves garbage on disk without knowing it is there
  (section 8).

## 3. Constraints

- One operator, one agent, one bot (spec 14). Tools are for admins
  (ADR-0031); a `family` member gets no commands.
- Core and edge fit a ~128 MB container (principle 8). The sandbox must
  not add a resident process.
- New dependencies: a release at least ~2 weeks old, an ADR, measured.
- It must work on the owner's Linux machine, in a Railway container and on
  a Pi, or switch itself off where it cannot be made safe (section 5).
  Kernel features differ between them (Landlock, user namespaces); this is
  the spec's open decision "Sandbox for scripts and MCP modules".
- The turn timeout is 3 minutes (`core.DefaultTurnTimeout`). A `du` over a
  home directory took the other assistant several minutes.
- The existing mechanisms are reused, not duplicated: the policy
  middleware (`policy.Decide`), pending calls and buttons that resume a
  turn (ADR-0032), task grants (R-POL-8), the tool-call journal
  (R-OBS-5), taint (R-POL-6), kill switches (R-POL-5).
- M3d comes after M3c (owner). M3c builds the simple runner for skill
  scripts (a subprocess, an environment allowlist, a timeout, decision
  D-5); M3d adds the sandbox to that runner, moves skill scripts onto it,
  and its sandbox ADR closes the spec's open decision (call).

## 4. The command tool

### 4.1 What the agent calls

- One tool, `host.exec` (name: call): a program and its arguments as a
  list, an optional working directory, an optional timeout. No shell
  string: the program is started directly, so `;`, `|`, `$(…)` and globs
  mean nothing (call).
- The tool runs on the node that serves the conversation, as the node's
  Unix user (owner: "on my behalf" locally, the service's user in the
  cloud).
- Output (stdout and stderr) is capped, the head and the tail are kept,
  secrets are redacted the same way as in the journal (call).
- Output is wrapped as external data and taints the session (call): file
  names, logs and file contents are text someone else may have written.
  The taint costs no extra presses for the work itself, since changes
  need a press anyway (4.2); it does make later memory writes and sends in
  that session ask, as for a web page.
- Every call is journalled: argv, working directory, exit code, duration,
  output size and head, the policy decision (R-OBS-5).
- A kill switch cell turns the whole tool off (R-POL-5).

### 4.2 Classes, decided in code

The class comes from a table in Go code, keyed by the program and, where
it matters, its subcommand and flags (owner: rules in code; table: call).
The model never chooses the class.

| Class | Examples | What happens |
|---|---|---|
| **Read** | Sizes, names and state only: `df`, `du`, `free`, `ls`, `ps` without environment or full command lines, `top -bn1`, `uptime`, `docker ps`, `docker images`, `docker system df`, `journalctl --disk-usage` | Runs by itself, no plan needed (owner: building a disk tree needs no press) |
| **Content** | Anything that prints what is inside files or other processes: `cat`, `head`, `tail`, `grep`, `less`, `journalctl` with entries, `docker logs`, `docker inspect`, `ps e`/`eww`, `go env` | Needs the owner's press, like a change (owner, 2026-11-03: otherwise other projects' `.env`, `~/.aws`, `~/.kube/config`, `~/.claude/.credentials.json` or other processes' environments reach the model unasked) |
| **Change** | `rm`, `go clean`, `cargo clean`, `docker image prune`, `docker builder prune`, `docker rm`, `docker volume rm`, `systemctl --user stop`, `kill`, `journalctl --vacuum-*`, any program not in the table | Needs the owner's press: a step of an approved plan (section 7) or a single-step confirmation (owner) |
| **Script** | `bash`, `sh`, `zsh`, `python3`, `node`, `perl`, `env` with a program, `xargs`, `find … -exec/-delete` | A change, only as an explicit plan step; the press shows the whole script. An approved script has every right of the node's user except root, and the plan says so next to it (owner) |
| **Forbidden** | `sudo`, `su`, `doas`, `pkexec`, `env`/`printenv` without a program; everything that starts a process outside the sandbox or leaves something to run later: `docker run`, `docker exec`, `docker create`, `docker compose up`, `systemd-run`, `systemctl --user start`/`enable`/`edit`, `crontab`, `at` (owner) | Refused in code; no press opens it (owner) |

- **The press is the boundary for what the exact command shows**
  (owner). The sandbox keeps unapproved commands from reading secrets; an
  approved command does what its argv says, as the node's user. What would
  lead out of the sandbox is Forbidden, press or not.
- **Every entry carries a fixed consequence line** in plain words (owner:
  the owner may not know what Docker is or why a command is risky). It is
  written in the table, in English, Russian and Ukrainian like the command
  hints, so a model under injection cannot soften it. Examples:
  - `docker image prune -af`: "Deletes Docker images no container uses.
    They are downloaded or built again when needed; nothing else is lost."
  - `docker rm <id>`: "Deletes containers. Files kept inside a container
    and not in a volume are lost for good."
  - `rm -r <path>`: "Deletes the folder and everything in it for good; it
    does not go to a trash bin."
  - a program not in the table: "ganglion does not know this program and
    cannot say what it will do."
  - a script: "Runs a script with all your user's rights except root;
    read it before pressing."

  The model adds its own one-line reason (why this step), shown apart from
  the fixed line and marked as the agent's words (call).
- **Files git ignores** (owner, 2026-11-03): an ignored file is often a
  secret (`.env`, a local config with keys) but just as often what the
  owner needs (build caches, logs). So ignored paths are not hidden: their
  names and sizes are read class like any other. A content command on a
  file that git ignores in its repository adds a warning to the press:
  "git ignores this file in <repo>; such files often hold passwords and
  keys".
- Unknown programs are **Change**, never Read (default deny, call).
- Docker is reached through its command-line tool; only the subcommands in
  the table run (call). A user in the `docker` group is as good as root to
  a script, which is one more reason scripts need a press and say what
  they may do.
- A read-class program with a flag that writes (`find -delete`, `du`
  has none, `docker system df` has none) is classified by that flag
  (call; the table lists the writing flags per program).
- The owner may move entries in the table through the node configuration,
  which the agent cannot edit (call; M4 brings the node YAML, M6 its
  changes with approval). Until then the table is code. Moving anything
  towards Read is a widening of rights and needs the owner (security-first
  rule).

### 4.3 Long commands

- A command may run longer than a turn (a `du` over a home directory).
  Past a short wait (call: 20 s) the call returns "running" and the
  command continues in the background; its completion is an event in the
  same conversation that resumes it with the result, the same way a
  confirmation press resumes a turn today (ADR-0032) (call).
- The plan message shows "⏳" on that step meanwhile.
- A command has a hard limit (call: 15 minutes, set lower per call);
  past it the whole process group is killed.
- At most a few commands run at once per node (call: 2); a third waits.

### 4.4 The trust boundary and abuse cases

| Abuse | Answer |
|---|---|
| A page or a file tells the model to run `curl … \| sh` | Not Read; needs the owner's press showing the exact command. The session is tainted, so nothing runs on the model's word alone |
| The model reaches root | `sudo` & co. are Forbidden in the table, and the node sets `no_new_privs`, so a set-uid program gains nothing even from inside an approved script (section 5) |
| The model reads the node's secrets | The sandbox hides them (section 5); the output is redacted on top |
| A command escapes the sandbox through a daemon (`docker run -v /:/h`, `systemd-run`, a crontab or autostart entry) | Forbidden in the table, with or without a press |
| The model hides what a step does behind soft words | The consequence line comes from the table, not from the model |
| The model does more than the plan said (the 19 containers) | A Change command outside the approved plan is not run; it gets its own button (section 7) |
| A scheduled run is steered by what it read | It may run only the exact commands listed in its grant (section 7.3) |
| The model edits its own rules | The table is code or node configuration outside the workspace; no tool writes there |
| Output floods the context or the journal | Size caps; head and tail kept |
| A fork bomb or a runaway process | Its own process group, a time limit, a cap on parallel commands; CPU and memory limits where the platform has them (builder) |

## 5. The sandbox

Owner: `sudo` is forbidden; `env` and the rest too, once it was clear
that reading is also dangerous. Call: the guarantees below are
requirements; how to meet them on each platform is the sandbox ADR, which
also closes the spec's open decision on a sandbox for scripts.

Every command:

1. **Cannot gain privileges.** `no_new_privs` is set, so `sudo` and any
   set-uid program run without their privileges. The node may simply set
   it on itself at start, since it never needs root, and every child
   inherits it (builder's choice).
2. **Gets a clean environment.** Only a fixed allowlist (`PATH`, `HOME`,
   `LANG`, `TZ`, `TERM=dumb`, call), never the node's variables.
3. **Cannot see the node's process.** Otherwise `/proc/<node pid>/environ`
   gives the node's secrets back to a process of the same user. A separate
   PID namespace is one way; the ADR compares others.
4. **Cannot read the node's data or secret files**: the data directory
   (database, logs, workspace git), the token and key files (`*_FILE`),
   and `.env` and `.env.*` files in any folder, not only the node's
   (owner, 2026-11-03). On the owner's machine also `~/.ssh`, `~/.gnupg` and the
   config folders of other secret stores (call: a default list the owner
   can extend).
5. **Has limits**: time, its own process group, and memory and CPU caps
   where the platform allows.

Root (call): if the node runs as uid 0, as a container does unless its
image sets `USER`, `host.exec` is off ("commands are off: running as
root"); `no_new_privs` takes nothing away from root. The M4 container
image (task 4.6) sets an unprivileged `USER`. Root-owned files stay for
the owner to clean by hand.

Fail closed (call): if the node cannot establish 1–4 at start (an old
kernel, no Landlock, a container without user namespaces), `host.exec` is
not offered to the model at all, the node logs why, and `/status` or the
basic stats say "commands are off: <reason>". The basic stats in section
6 do not depend on the sandbox.

Note for the ADR: Landlock grants access to whole trees and cannot carve a
denied folder out of a granted one; hiding `~/.ssh` while allowing `~`
means granting each sibling, or a mount namespace that covers the denied
paths. The local kernel is 7.0 with Landlock and Yama
(`ptrace_scope=1`) and unprivileged user namespaces on; Railway and a Pi
must be checked.

## 6. Basic stats without commands

Owner: the cloud minimum is free memory, the agent's own memory and disk
space, what `top` shows an ordinary user.

- A read-only tool (`host.stats`, call) implemented in Go, not as a
  command: memory (total, available, the node's RSS), load, uptime, disk
  free and used for `/` and for the data directory, and the sizes of the
  node's own folders (database, logs, workspace, the claude CLI work
  directory, module store).
- It needs no sandbox and no subprocess, so it works everywhere, including
  where `host.exec` is off (call).
- It runs by itself, for admins, and does not taint the session (call: it
  prints numbers and the node's own paths, nothing someone else wrote).

## 7. Plans

### 7.1 The plan message (owner)

- Before work with several steps, the agent posts a plan: numbered steps
  in plain words; each step that needs a press shows its exact command,
  the fixed consequence line from the table (4.2) and the agent's one-line
  reason (owner).
- The same message is edited as the work goes: ⏳ running, ✅ done,
  ❌ failed with one line why, ⏭ skipped.
- At the end it carries the outcome (e.g. "disk: 4 GB free → 47 GB") and
  stays in the chat as the record of what was done (call; the other
  assistant deletes its plan, the owner did not ask for that).
- Edits are throttled to stay inside Telegram's limits (builder).
- Tools (call): `plan.show` with the steps, `plan.step` to mark one. The
  transport only edits a message; it knows nothing about plans.
- Asking the model to plan is a directive in the prompt (layer 2): code
  does not force a plan for read-only work.

### 7.2 The plan is the approval (owner)

- If the plan holds no step that needs a press, it has no button; the
  agent shows it and starts (owner).
- If it holds such steps, it has one "Run" / "Cancel" pair. One press
  approves exactly the listed commands, argv for argv, for this
  conversation, until the plan ends or expires (call: 1 hour). "I already
  said so" is that press (owner).
- A Change command that is not in the approved plan does not run. The
  agent either asks for it alone (a one-step confirmation, as today) or
  posts a revised plan with a new button (call). This is the 19
  containers case.
- Approval goes through the existing pending-call machinery: a random
  one-time button id, only the addressee may press, a double press runs
  once, no answer within the TTL denies, the press resumes the turn
  (ADR-0032).
- Exact argv, no patterns (call). When a step depends on what an earlier
  step finds (which container ids), the agent shows a new plan after it
  has looked.

### 7.3 Scheduled tasks (owner)

- A task's grant (R-POL-8) gains a list of commands: the exact argv the
  runs may execute, shown in the creation confirmation with their
  consequence lines, e.g.
  `docker image prune -af` every Sunday. The owner approves it once; the
  runs need no presses (owner: "the scope and the impact are known").
- Read commands are allowed in any run whose grant includes `host.exec`
  (call); Change and Script commands only if listed.
- A run that needs a command outside its grant is denied, and the owner
  gets one notice with the reason (as R-POL-8 does today).
- Example the acceptance uses: a nightly check that reports disk and
  memory with `host.stats` and `docker system df`, and a weekly cleanup
  task with `go clean -cache` and `docker builder prune -f` in its grant.

## 8. The node knows its own garbage (owner)

From the same brainstorm; recorded here because it is the other half of
"where did the space go".

- **Principle** (owner agreed): everything the node creates on disk is
  accounted for (what, size, age) and cleaned by age or size. `host.stats`
  (section 6) reports it. Today's example is the claude CLI work
  directory, already a deferred minor of M3a; its cleanup lands with M3d
  (call).
- **Build caches in P3** (owner): when the agent compiles modules, each
  module has its own build cache (in Docker, a volume labelled with the
  module). Every build during active work renews its TTL; after a while
  without builds (call: 7 days, operator-set) the cache is dropped and the
  module, its sources and its signed build stay. Shared caches (crates,
  Go modules, toolchain images) are trimmed by size, least recently used
  first. Dropping a cache runs by itself and reports one line ("dropped
  the cache of module X, N GB freed"). This goes into spec 9.4, not into
  M3d.
- Development side, already done on 2026-11-03: `make clean-modules` and
  a lean dev profile for `html2md` (2.5 GB → 242 MB after a full test
  run).

## 9. Files sent to the agent (owner, 2026-11-03)

A file or a picture the owner sends is dropped today: the transport keeps
only the caption (`transport/telegram/transport.go`), so a PDF with
"check this, something is suspicious" reached the model as the caption
alone, and the agent ran a memory audit instead.

- **M3c** (owner): the transport tells the agent that a file came (name,
  type, size) and that it cannot open it yet, so the agent says so
  instead of guessing.
- **Later, its own brainstorm after M3d** (owner): reading files and
  pictures: storage with the same accounting and TTL (section 8), which
  models can read pictures and PDFs, files as external data (a planted
  PDF is a classic injection path), and checks with M3d's commands
  (`file`, an antivirus).

## 10. Acceptance of M3d

Scripted with the fake Bot API and a scripted model where a real model
would make the check flaky, then once live on the test bot.

1. "Build me a tree of what takes space under ~/project" → read commands
   only, no button, the tree in the reply; a long `du` continues in the
   background and its result arrives in the same conversation.
2. "Clean the Go cache and the Docker images" → a plan with the exact
   commands, their consequence lines and one button; after the press the steps run and are ticked;
   the plan ends with disk before and after.
3. During an approved plan the model asks to run `docker rm …` that was
   not in it → not run; a separate button appears.
4. `sudo true` is refused by the table; a script in an approved plan that
   calls `sudo` fails to gain root (`no_new_privs`).
5. From a command, the node's secrets cannot be read:
   `cat /proc/<node pid>/environ`, the database file, a `*_FILE` secret,
   `env`. Each attempt fails or shows nothing secret.
6. `docker run …` and `systemd-run …` are refused even inside an approved
   plan; `cat` of a file asks for a press.
7. A weekly task with `docker image prune -af` in its grant runs without a
   press; the same task trying `docker rm` is denied with one notice.
8. A fetched page that says "run `curl … | sh`" leads to a button with
   that exact command at most, never to a run.
9. In a 128 MB container where the sandbox cannot be established,
   `host.exec` is not offered, `host.stats` works, and the reason is
   reported.
10. Memory: the node's idle and peak RSS stay inside the budget with the
   tool on (`make memory`).

## 11. Spec and documents to change first

- Spec: a new section 7.12 "Host commands and plans" with `R-CMD-*`
  requirements from sections 4–7; R-POL-8 gains commands in the grant;
  9.4 gains the build-cache TTL (section 8); a requirement that the node
  accounts for and cleans its own files; the open decision on the sandbox
  points at the new ADR; "Not now" gains plan grants for tools other than
  commands and a cloud MCP client earlier than P3 if the owner wants it;
  reading files and pictures sent to the agent (section 9). Phase plan
  for M3c: the "a file came" notice.
- Phase plan: a milestone M3d after M3c, with its tasks; spec 10.2's
  scope sentence mentions it.
- ADRs (Proposed, accepted by the owner):
  1. host commands: argv without a shell, classes in code, output as
     data, background completion;
  2. the command sandbox, closing the open decision (options: Landlock +
     PID namespace, bubblewrap or nsjail as a binary, a separate uid);
  3. plans as approvals: the plan message, the plan grant, commands in
     task grants.
- Architecture views and the code map in the same commits as the code.

## 12. Left to the builder

- Package and tool names within the conventions; where the class table
  lives (`policy` or a package of its own).
- The full class table and its consequence lines, the environment
  allowlist, the default deny list of secret paths.
- Output caps, the background threshold, time limits, parallelism.
- How each sandbox guarantee is met per platform, within the ADR.
- Throttling of plan edits.
- Test strategy per task, in the plan.
