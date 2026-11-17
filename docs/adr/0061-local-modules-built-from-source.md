# ADR-0061: Build `local/…` modules from source through a builder, install them on the owner's press, and journal every version

- **Status:** Accepted
- **Date:** 2026-11-10
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-1, R-RUN-8, R-RUN-9, R-RUN-10), [7.12](../spec.md#712-host-commands-and-plans), [9.4](../spec.md#94-skill-compilation-p3), [10.2](../spec.md#102-p1--all-in-one--scope-and-acceptance); [M4 design](../design/p1-m4-reflexes-design.md) section 6; extends [ADR-0041](0041-module-install-update-and-trust-boundary.md); [ADR-0033](0033-memory-in-the-workspace-git-with-the-git-binary.md), [ADR-0039](0039-command-sandbox.md), [ADR-0056](0056-small-hosts-as-a-soft-target.md)

## Context

ADR-0041 settled built-in modules (`ganglion/…`): they come only from the
project's GitHub Releases, checked against a ceiling the core embeds. It
reserved `local/…` for modules "built later by the agent", always with
the owner's approval, and left two things open: a second source of
modules, and the worded approval of a widening (R-RUN-8).

M4 brings the first such module. `deye-reader` reads one owner's
inverter; it is not part of the agent (owner, 2026-11-09). In M4 its
source is an example in the repository; in P3 the agent writes the
source itself (spec 9.4).

Facts that shape the choice:
- The workspace is a git repository (ADR-0033). Source kept there has a
  history the owner can read and the agent can change.
- ADR-0041 said the node must not compile modules: a toolchain is hundreds
  of MB and slow on a Pi. ADR-0056 since made small hosts a soft target:
  a feature that needs more runs where the resources are, by the
  operator's choice. A Rust toolchain with `wasm32-wasip1` makes the image
  about 1–1.5 GB, and a build needs about 1 GB for a minute.
- Spec 9.4 puts builds on a worker in P3. P1 has one process and no
  workers.
- M3d gave the node a command sandbox (ADR-0039): no node secrets, the
  node's data and the owner's secret stores unreadable, a time limit.

Two questions: how a `local/…` module gets onto the node, and where it is
built.

## Options

How a module gets in:

### A. A `.wasm` sent as a file in the chat

The owner, or someone the owner forwards from, sends a module; a press
installs it.

### B. A URL plus a sha256

R-RUN-8's wording for P1: the node fetches a file and checks its sum.

### C. Built by the agent from source in its workspace

The source lives in `workspace/modules/<name>/`. The agent builds it with
a tool, and the owner approves the install. The node never accepts a ready
`.wasm` for a `local/…` name.

Where builds run:

### D. In the node

A `local` builder runs the toolchain as a separate process in the command
sandbox.

### E. A separate builder worker

A worker marked `builder` (P2) builds and returns the artifact over the
bus.

### F. GitHub Actions

The source is pushed to a repository; a workflow builds it and the node
fetches the result.

## Comparison

| Criterion | A. File in chat | B. URL + sha256 | C. From source |
|---|---|---|---|
| What the owner can read before the press | An opaque binary | An opaque binary | Source and its git history |
| Provenance | Whoever sent it | Whoever controls the URL | A workspace commit, recorded |
| A forwarded or injected module | Installable after one press | Installable after one press | No path in |
| Path to P3 (the agent writes the module) | None | None | The same path |
| Needs a toolchain | No | No | Yes |

| Criterion | D. In the node | E. Builder worker | F. GitHub Actions |
|---|---|---|---|
| Available in P1 | Yes | No (P2) | Yes |
| Image and memory | Toolchain in the image; about 1 GB during a build | On the worker only | None on the node |
| What comes back to the node | A file it built itself | A file a trusted node built | A ready `.wasm` from outside, which C forbids |
| The workspace leaves the node | No | To the worker | To GitHub, with a token |
| Works offline from GitHub | Yes | Yes | No |

## Decision

**C, built in the node (D) for now, behind one interface that E will
implement** (owner, 2026-11-09). Source in git is something the owner can
read and the journal can point at; a binary from outside is neither. The
node builds because P1 has nothing else to build on.

### The builder

```go
type Builder interface {
    // Can reports the languages this builder can build, with versions.
    Can(ctx context.Context) (Abilities, error)
    // Build compiles a module's source into one .wasm; the log is for the agent.
    Build(ctx context.Context, src Source) (Artifact, Log, error)
}
```

- M4 has one implementation, `local`. The node uses it unless a worker
  marked `builder` exists (P2); the owner and the agent see no difference.
- Every build runs in the M3d command sandbox, with a time limit and its
  target folder (`CARGO_TARGET_DIR`) in a temporary folder removed
  afterwards: one `.wasm` comes out, no build garbage stays. See
  [ADR-0064](0064-builds-write-only-their-temp-folder.md): that folder is
  the only one a build may write, and what it leaves running is killed.
- `Can` checks what the machine has: `cargo` with `wasm32-wasip1`,
  `tinygo`, `go`. A module in a language no builder can build gets "no
  builder for Rust on this node".
- Dependencies come from crates.io or the Go proxy, pinned by the lock
  file's checksums. The build runs with `--locked` (or `-mod=readonly`), so
  it never resolves a version the lock file does not name.
- **`module.build` asks no press.** It reads the workspace, writes only its
  temporary folder and runs in the sandbox without the node's secrets.
  What runs in the node is guarded by the install press, which names the
  checksum of exactly the artifact the build made.
- This reverses ADR-0041's "the node does not compile modules" for
  `local/…` modules only. Built-in modules still come only from the
  project's releases.

### Build, approve, install

1. `module.build` finds the module's language, checks `Can` and builds. A
   failure returns the build log to the agent.
2. `module.install` checks, before it asks:
   - the name is `local/…` and not a reserved name
     ([ADR-0062](0062-cells-owned-by-modules.md));
   - every secret the manifest names is set;
   - every required config key is given;
   - no cell the module would create belongs to another owner
     ([ADR-0062](0062-cells-owned-by-modules.md));
   - the module loads, and its `health`, if exported, passes.
3. The owner sees the install window: name, version, size and sum; network
   hosts, secrets, cells created and read, triggers and tools, limits;
   `[Install]` and `[Reject]`. An update whose rights are wider than the
   active version's opens with the widening on its own line ("New: network
   api.example.com"), as R-RUN-8 asks. A first install that finds the
   cells of an earlier module of the same full name says it "takes over N
   existing cells with their history" (ADR-0062).
4. The press installs and starts the module at once. The files and the
   version go to ADR-0041's store (`<data>/modules/<name>/<sha256>.wasm`,
   `module_versions`); `ganglion.yaml` gets a `modules:` line with the
   name and the config only ([ADR-0059](0059-node-config-file.md)).

The owner's commands, without a model: `/modules`, `/modules <name>` (the
card and the journal), `/modules config <name> key=value`, `/modules
off|on <name>`, `/modules rollback <name>`, `/modules remove <name>`.

### The journal

Every version stays a row of `module_versions` for good; the files stay
two, the active and the previous (ADR-0041). A row records:
- who built it, at whose request;
- why: a note written by the agent at build time, or by the owner. Never
  text from the module or its manifest;
- the workspace commit it was built from;
- counters: runs, failures, exceptions, the last failure's kind and time.

## Security

- **Trust boundaries:**
  - the workspace source and its dependencies, which a build turns into
    code that runs in the node's process;
  - the build process, which runs third-party code (build scripts,
    macros) on the node;
  - the module at run time, as in ADR-0041, now with rights.
- **What an attacker controls:** a dependency on crates.io or the Go
  proxy; the module's code and manifest (in P3 written by a model that may
  have read an injected page); a file someone sends the bot; the text a
  module writes.
- **Abuse cases and what stops them:**
  - *A ready `.wasm` sent in the chat or offered by URL.* No tool takes
    one: `module.install` installs only an artifact this node's builder
    produced from a workspace commit.
  - *A malicious or swapped dependency.* The lock file pins each
    dependency by checksum and the build runs `--locked` (or
    `-mod=readonly`), so a changed upload or an unpinned version fails the
    build. A dependency that was malicious when it was locked is not
    caught; its
    build code runs in the sandbox without the node's secrets, data or the
    owner's secret stores, and the module it produces gets only the rights
    the owner approved in the window.
  - *A module asks for more than it needs.* Nothing is granted that the
    manifest does not list (R-RUN-1); the window lists every right, and a
    widening has its own line.
  - *A module plants text for the model or the owner* (an error message, a
    manifest field, a note). The reasons and notes the model and the owner
    see are the node's own words or the agent's and owner's; what the
    module or its manifest wrote goes only to the log (ADR-0041). A
    cell's `about` goes to the owner in `/cells`, never to the model. What
    a module returns through its tool or its cells taints the session only
    where it is a string (R-RUN-13, [ADR-0062](0062-cells-owned-by-modules.md)):
    a module hands over contract data and interprets what it fetched
    itself.
  - *An install swapped between the window and the press.* The window
    names the artifact's checksum, and the press installs exactly that
    artifact.
  - *A module claims another module's cells.* Refused at install, and its
    writes are prefixed with its own name (ADR-0062).
  - *The agent installs without the owner.* `module.install` only opens
    the window; only the owner's press installs.
  - *A `local/…` module takes a built-in's place.* Built-in names come only
    from releases (ADR-0041); `module.install` accepts only `local/…`.

## Consequences

- ADR-0041's "the node does not compile modules" no longer holds for
  `local/…` modules. ADR-0041 is accepted and not rewritten; when this ADR
  is accepted, its row in the index gets "one rule superseded by 0061".
- R-RUN-8's "a URL plus a sha256" stays for built-in modules only.
- Builds go through the `Builder` interface (spec 9.4, R-RUN-9): the node
  builds unless a worker marked `builder` exists. A node without a
  toolchain (a small Pi) cannot get `local/…` modules until one exists.
- The image carries `git` and Rust with `wasm32-wasip1`, about 1–1.5 GB.
  The container needs room for a build, and `node.memory_limit` is set
  explicitly.
- Every build compiles from a clean target folder: slower, but nothing
  stays behind. The per-module build caches of spec 9.4 are not built in
  M4.
- `module_versions` grows by one row per version, for good.
- Builds, installs and refusals get metrics (design section 8.6).
- Residual risk of a build without a press: the dependencies' build
  scripts and macros run with network access inside the sandbox. They
  cannot read the node's secrets or data, but they can reach what the
  sandbox grants and send it out. The install press does not cover this;
  the lock file and `--locked` narrow it to the versions the source
  names.
- `ganglion.yaml` and the store share only the module's name; an update
  or a rollback touches the store, never the file.

## Revisit when

- A builder worker exists (P2).
- Signatures land (P3): a built module is signed before install.
- A build on the node takes too long or too much memory to be practical.
- A supply-chain incident in a dependency the modules use.
