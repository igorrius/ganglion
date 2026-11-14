# Security policy

ganglion runs code a model wrote, runs commands on its host and reads text
from strangers. Its design rule is that every link is hostile and every
input is untrusted, and that what stops an abuse is code, not a prompt
([spec, principle 9](docs/spec.md#2-principles)). A report that shows where
the code does not hold that line is the most useful one the project can
get.

## Reporting a vulnerability

Report it privately, through
[GitHub's private vulnerability reporting](https://github.com/igorrius/ganglion/security/advisories/new)
("Report a vulnerability" on the repository's Security tab). Do not open a
public issue for it.

Say what you did, what happened and what should not have: a configuration,
a message or a module that reproduces it is worth more than a description.
Leave out real tokens and the address of any live node.

The project is 0.x and has one maintainer, so there is no response time to
promise and no bounty. You will get an answer, a fix on `master` when there
is one, and credit in the advisory if you want it. There are no supported
old versions: a fix lands on `master` only.

## In scope

- **The module sandbox.** A WASM module that reaches anything its manifest
  did not ask for and the owner did not approve: a host, a file, a secret,
  another module's state, more memory or time than its limit.
- **The command sandbox.** A host command or a skill's script that escapes
  its restrictions: reads the node's database, its key or its secrets,
  gains privileges, or reaches what its class does not allow.
- **Secrets.** A secret that appears in a log, the journal, a metric, an
  error a model reads, a message, or a child process's arguments; a weakness
  in the encrypted store.
- **Prompt injection that crosses a boundary in code.** Outside text (a web
  page, a file, a message in a group, a module's output) that makes the
  node run a state-changing action without the owner's confirmation, send
  private data out, or change its own configuration, memory or skills.
- **Access.** A person who is not on the access list getting an answer, a
  member of a group getting an admin's rights, a forged webhook call that
  is accepted.
- **The node's listeners** and the diagnostics server (`ganglion mcp`).
- **The release path of modules:** a module accepted with a wrong checksum
  or with rights above the node's ceiling.

## Out of scope

- A model saying something wrong or rude with no boundary crossed.
- What the owner approved with a button, or what someone can do who already
  has the node's files, its shell or its admin role.
- Parts the spec marks as not built yet.
- Denial of service that needs an account on the access list.
- Vulnerabilities in dependencies with no path to exploit them through
  ganglion: report those upstream.

## Running a node safely

Run it as an unprivileged user, give it its own bot token and its own
provider keys, and keep its data directory private: it holds the
conversations, the memory and the key of the secret store.
