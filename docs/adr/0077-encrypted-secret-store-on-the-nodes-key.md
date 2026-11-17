# ADR-0077: Keep secrets in an encrypted store on the node's own key

- **Status:** Accepted
- **Date:** 2026-11-13
- **Deciders:** Igor Artasevych
- **Spec:** [7.14](../spec.md#714-node-configuration) (R-CFG-2, R-CFG-9, R-CFG-10), [7.5](../spec.md#75-runner-and-wasm-modules) (`secret.get`, R-RUN-9), [7.8](../spec.md#78-security-and-operations-mvp), [11](../spec.md#11-open-decisions); task 6.5 of the [P1 phase plan](https://github.com/igorrius/ganglion/blob/e6b25054290c0a0e460edf0639b5e8dfa761f251/docs/plans/p1-all-in-one.md); [design note](../design/secrets-intake-design.md) (board card G-149); [ADR-0059](0059-node-config-file.md), [ADR-0031](0031-agent-security-baseline.md), [ADR-0016](0016-sqlite-migrations.md), [ADR-0039](0039-command-sandbox.md)

## Context

Every secret is an environment variable today (R-CFG-2): the bot token,
the providers' keys, and each secret a module's manifest names. The Deye
module alone needs four. On Railway a new secret is a new variable and a
redeploy.

This does not scale past the owner (owner, 2026-11-13):

- a person who runs a node may have no access to the environment it
  started in, or no knowledge of how to set a variable there;
- the agent writes modules (ADR-0067), and each one would ask the owner to
  create variables by hand, with names the agent made up;
- two modules that name the same variable read the same value.

ADR-0059 left room for a second secret source beside `env:NAME`, and
package `secrets` has one interface for it. The owner's brainstorm of
2026-11-12 sketched a store and a way to hand a value over from the chat
past the agent. This ADR decides the store. The way a value reaches it
from the chat needs a spike first and is a separate ADR.

The rules that already hold and must keep holding: the model never sees a
secret, a module reads one only by its manifest, secrets are redacted
before the journal and the logs (ADR-0031), and a host command cannot
read the node's secrets (ADR-0039).

## Options

### A. The environment only

As today. Nothing to build.

### B. Plain files in a folder on the volume

One file per secret under the data directory, mode `0600`, the folder
denied to commands. The node reads a file at each use.

### C. An encrypted store in the node's database, on a key the node makes

Values are encrypted rows in SQLite. The master key is a file the node
creates at its first start; the operator may supply it instead. The
environment stays as a source.

### D. An outside secret manager

Vault, the platform's own API (Railway variables written by the node), or
a `sops`/`age` file in the config repository.

## Comparison

| Criterion | A. Environment | B. Plain files | C. Encrypted store | D. Outside manager |
|---|---|---|---|---|
| A secret set without a restart | No | Yes | Yes | Depends; Railway redeploys |
| A person without host access | Stuck | Stuck until a chat path exists | Stuck until a chat path exists; nothing else to set up | Needs an account and its setup |
| A copy of the database or a backup leaks | Holds no secrets | Holds no secrets; the folder's backup holds them in the clear | Holds ciphertext only | Holds no secrets |
| Secrets scoped to a module | By naming only | By folder | By a column, bound into the ciphertext | By the manager's paths |
| One place for names, dates and who reads what | No | The file system | One table | The manager |
| New dependency or trusted party | None | None | None: the standard library's AES-GCM | A service, its client, its credentials |
| Works on Railway, a Pi and a laptop alike | Yes | Yes | Yes | No |
| Effort | None | Small | Small to medium | Large |

## Decision

**C, the encrypted store, with the environment kept as a source** (owner,
2026-11-13). It asks nothing of the person who runs the node, adds no
trusted party, and puts names, dates and readers in one place. B is the
same work without protecting backups.

- **The master key.** 32 random bytes. The node reads
  `GANGLION_MASTER_KEY` or `GANGLION_MASTER_KEY_FILE` through the
  environment source; with neither set, it uses `master.key` in the data
  directory and creates it (mode `0600`) at the first start. So a node
  needs no setup, and an operator who wants the key off the volume
  supplies it.
- **The store.** A table in the node's database, through the migrator of
  ADR-0016: scope, name, nonce, ciphertext, when it was set and by whom.
  A value is at most 4 KB.
- **The cipher.** AES-256-GCM from the standard library, a random nonce
  per value. The scope and the name are the additional data, so a row
  copied under another name or scope does not decrypt.
- **Scopes.** `module:<name>` holds a module's secrets; `node` holds the
  node's own. A module's `secret.get(NAME)` reads its own scope, then the
  environment variable `NAME` as before. No module reads another module's
  scope or the `node` scope. Removing a module removes its scope; an
  update, a rollback, `off` and `on` keep it.
- **The config file.** A secret field takes `store:NAME` beside
  `env:NAME` (R-CFG-2); `store:NAME` reads the `node` scope. A literal
  still fails the start.
- **Setting a value.** `ganglion secret set [--module <name>] NAME` reads
  the value from standard input, never from an argument; `ganglion secret
  rm` and `ganglion secret list` complete it. It works while the node
  runs: the node reads the store at each use and keeps no copy, so the
  next read sees the new value with no restart.
- **Reading back.** Nothing shows a value: not a command, not a tool, not
  the chat. `/secrets` (admin, private chat, no model) lists the names by
  scope, the dates and the modules that name each, and removes one.
  `module.status` and `config.show` keep showing set or unset.
- **Redaction.** Every value the store hands out is registered with the
  redactor before it is returned, so a value set after the start is
  hidden in the journal and the logs like one read at the start.
- **A key that does not fit.** The store keeps a check value encrypted
  with its key. When the key the node has does not decrypt it (the file
  was lost, or another key was supplied), the node never makes a new key
  over existing rows. It starts with the store locked: stored secrets
  read as unset, setting one is refused, `/status` and `/secrets` give
  the reason. `/secrets reset` (admin, with a confirmation) drops every
  row and adopts the key the node has.
- **What stays in the environment.** Whatever the operator points at with
  `env:NAME`, and the master key when supplied. A first install still
  needs the bot token from outside: there is no chat before it.

Not decided here: how the owner hands a value over from the chat (the
intake page of the design note, after a spike on Telegram's `sendData`),
and moving the claude CLI's token into the store (the `/login` note,
G-151). Both build on this store.

## Security

- **Trust boundaries:** one new file, the master key; who reads it reads
  every stored secret. The store itself adds none: it is in the database
  the node already owns. The `secret` commands run on the host as the
  node's user, who can already read the node's environment.
- **What an attacker controls:** a copy of the database or a backup; a
  module's code and its manifest; the agent, steered by outside text; a
  host command inside the sandbox; a reader of `ganglion mcp`.
- **Abuse cases and what stops them:**
  - *A database copy or a backup is read elsewhere.* It holds ciphertext;
    the key is not in the database.
  - *The volume is copied whole.* Nothing stops this when the key file is
    on the same volume. An operator who needs that supplies the key
    through `GANGLION_MASTER_KEY` or a file elsewhere.
  - *A module names another module's secret.* The lookup is by the
    calling module's scope; the scope is in the additional data, so a row
    moved in the database does not decrypt either.
  - *A module names the node's own secret.* The `node` scope is never
    read for a module, and the install still refuses a manifest that
    names one of the node's variables (R-RUN-9).
  - *A host command reads the key or the store.* The sandbox already
    denies the data directory and every `<NAME>_FILE` secret; the master
    key's variable joins the names it hides.
  - *The agent tries to read a secret.* No tool returns a value. The
    agent cannot set one either until the intake ADR says how.
  - *A value in an error, a log or `argv`.* Errors name the scope and the
    name; the value comes on standard input; a value handed out is
    registered for redaction first.
  - *The key is lost and the node quietly starts a new store.* The check
    value locks the store instead, and only the owner's `/secrets reset`
    drops the rows.
- **Not protected:** a compromised host or the node's own process. The
  encryption protects copies of the data, not a machine an attacker runs
  code on.

## Consequences

- A new module secret needs no variable and no redeploy. Until the intake
  exists it still needs the host: `ganglion secret set`.
- The Railway node moves its module secrets from variables into the store
  one by one; the environment fallback keeps it working meanwhile.
- A module's secret gets a scope, so the agent no longer has to invent
  names that are unique across the node.
- Backups (task 7.4) must treat the key apart from the database: a
  database restored without its key comes up locked and every secret is
  entered again. The cutover runbook says where the key is kept.
- The sandbox's deny list and `settings.File.SecretNames` learn the
  master key's variable; `config.show` learns `store:NAME`.
- R-CFG-2 changes: `store:NAME` is a second scheme. ADR-0059 is not
  rewritten; its "revisit when a second secret source arrives" is this
  ADR.
- To build (task 6.5): the key, the table and its migration, the store
  behind `secrets.Source` with scopes, redaction of values handed out,
  the `secret` commands, `/secrets`, the locked state, `store:NAME` in
  the settings and the schema, the module host's lookup order.

## Revisit when

- The intake from the chat exists: decide whether a module's fallback to
  an environment variable still earns its place.
- A second node appears (P2): a secret may have to reach a runner on
  another machine, and spec 9.3 already asks for secrets encrypted to the
  node's key.
- A value over 4 KB or a file is needed.
- Rotating the master key is asked for; nothing here rotates it.
