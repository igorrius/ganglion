# ADR-0042: Run skill scripts with a declared effect class, an environment allowlist and a timeout until the sandbox lands

- **Status:** Proposed
- **Date:** 2026-11-03
- **Deciders:** Igor Artasevych
- **Spec:** [7.3](../spec.md#73-model-router) (R-RTR-4, R-RTR-5), [9.8](../spec.md#98-self-modification-levels) (levels 1 and 2), decision D-5; design section 6.2 ([M3b and M3c design](../plans/p1-m3b-m3c-design.md)); [ADR-0031](0031-agent-security-baseline.md), [ADR-0039](0039-command-sandbox.md); plan task 8 ([M3c plan](../plans/p1-m3c-modules-skills-diagnostics.md))

## Context

Task 3.7 adds skills:
- level 1, Markdown procedures;
- level 2, a script beside the Markdown.

A script is arbitrary code on the host. The sandbox that confines what a
command can read (ADR-0039: `no_new_privs`, Landlock, a PID namespace,
fail closed) is M3d's first task. It needs a spike on three platforms
first.

M3c has to decide what a script may do before that. The pieces already
decided:
- only the owner installs a skill, by putting files in
  `workspace/skills/`;
- no tool writes there;
- the agent may only propose a skill's text.

What remains is when a script runs without a press, and what it sees.

## Options

### A. Scripts only in M3d

M3c ships level 1 only; level 2 starts together with the sandbox. The
3.7 acceptance moves to M3d.

### B. A press for every run

Any script run asks the owner, whatever it does.

### C. An effect class in the frontmatter

The script declares `effects: read` or `effects: change`. A read
script runs by itself, and a change script asks with a button. In both
cases the output is outside content, and every run gets the same
process limits.

## Comparison

| Criterion | A. M3d only | B. Press always | C. Declared class |
|---|---|---|---|
| Useful in M3c | No scripts | Yes, with friction | Yes |
| Scheduled tasks | — | Cannot run a script unattended | A read script in a grant runs; a change script follows the grant rules |
| Who declares the class | — | — | The owner, who wrote or installed the skill |
| Protection from a script reading files | Full, later | Only the owner's attention | None until M3d |
| Matches M3d's model | — | No | Yes: host commands have the same read/change classes (ADR-0038) |

## Decision

**C** (owner, 2026-11-03).
- It is useful now.
- Unattended tasks can use read scripts.
- The classes are the ones host commands will have.

What holds for every run, enforced by code:
- argv is the script's path, with no shell;
- stdin is the model's input, at most 16 KiB;
- the working directory is a fresh `0700` temp dir, removed after;
- the script runs in its own process group, killed whole at the timeout
  (default 30 s, at most 120 s) or on cancel;
- stdout is capped at 64 KiB and stderr at 8 KiB;
- the result is wrapped as data and taints the session.
- The environment is built from scratch:
  - `PATH`, `HOME` (the temp dir), `LANG` and `TZ`;
  - plus the names the frontmatter lists.
  - A deny list always wins, so a skill listing one is not loaded:
    `TELEGRAM_*`, `GANGLION_*`, `*_KEY`, `*_TOKEN`, `*_SECRET`,
    `*_PASSWORD`, `*_FILE`, and every provider's `key_env`.
- A script must be a regular executable file inside the skill's
  directory, not a symlink.
- Built-in skills are embedded and reserved by name: a workspace skill
  with the same name is not loaded, and the owner is told.

## Security

- **Trust boundaries.** The model's input crosses into a host process.
  The script's output crosses back to the model.
- **What an attacker controls.** Through a prompt injection, the input
  the model passes and when the model calls a script. Not the script
  itself, which only the owner installs.
- **Abuse cases:**
  - *An injected page makes the model call a change script.* It is a
    side effect, so the policy asks; in a tainted session it always asks.
  - *The input is crafted to exploit the script.* The script's own
    handling is the owner's code; the input is size-capped, and no
    shell parses it.
  - *A script exfiltrates the node's secrets from its environment.* They
    are never in it.
  - *A script reads the node's database or `*_FILE` secrets from disk.*
    **Not stopped until M3d.** A script can read what the node's user can
    read. Installing a script is trusting it with that, and the owner
    setup says so.
  - *A script hangs or floods.* Killed with its group at the timeout;
    output capped. A script can leave its group with `setsid`; only the
    M3d sandbox holds it then. Under PID 1 without an init, the children
    killed after a run stay zombies: run the node under an init.
  - *A script fails and prints an injection on stderr.* The failure is
    a result marked as outside content, not an error in the node's
    words.
  - *A typo in the model block fails open* (`privacy: local` would send
    a local-only skill to the cloud). Values are checked at load; a
    `local-only` turn stays local whatever skill is read next.
  - *A file is swapped after the start, or another local user can write
    it.* A script or skill directory writable by group or others is
    refused, and the checks run again before each run.
  - *A workspace skill shadows a built-in one.* Not loaded.

## Consequences

- Skills with scripts work in M3c, including in scheduled tasks.
- The owner carries the risk of the scripts they install until M3d,
  stated in the owner setup.
- M3d task 3.26 moves the same runner onto the ADR-0039 sandbox. The
  classes stay.

## Revisit when

The ADR-0039 sandbox lands (M3d task 3.26), or a skill comes from
anywhere but the owner's hand.
