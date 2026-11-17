# ADR-0064: Let a module's build write only its own temporary folder, and kill what it leaves running

- **Status:** Accepted (2026-11-09)
- **Date:** 2026-11-09
- **Deciders:** Igor Artasevych
- **Spec:** [7.5](../spec.md#75-runner-and-wasm-modules) (R-RUN-9), [7.12](../spec.md#712-host-commands-and-plans) (R-CMD-3); refines [ADR-0039](0039-command-sandbox.md) for builds only, which is accepted and not rewritten; [ADR-0061](0061-local-modules-built-from-source.md), [ADR-0057](0057-commands-may-list-split-folders.md); task 5b of the [M4b plan](https://github.com/igorrius/ganglion/blob/e6b25054290c0a0e460edf0639b5e8dfa761f251/docs/plans/p1-m4b-modules.md)

## Context

The command sandbox of ADR-0039 lets a command read and write everything
under `/` except its deny list. That suits `host.exec`: the admin's own
commands, each shown and pressed. A module's build (ADR-0061, `builder`)
is different: `cargo build` runs code the module's source brings, its
`build.rs` and its procedural macros, and nobody presses each thing that
code does. In M4 the source is an example in the repository; in P3 the
agent writes it.

With the ADR-0039 sandbox that code can:

- write `~/.rustup` or `~/.cargo/bin`, the owner's own toolchain, which
  every later build and the owner's own work run;
- write `/tmp/.cargo/config.toml` or `/.cargo/config.toml`: cargo reads
  `.cargo/config.toml` from every parent of its working folder, so one
  file sets the linker or a build wrapper of every later build;
- call `setsid()`, leave the build's process group, and keep running as
  the owner's user after the build ended, which the process-group kill
  of `procgroup` never reaches.

The owner decided (2026-11-09): builds run in a mode where writes are
allowed only beneath the build's own temporary folder; everything stays
as readable as today (the deny list still denied) and nothing else is
writable. The escaped process is to be handled too, or recorded as a
known limit.

What a build needs, measured on the owner's machine (Landlock ABI 7)
with `builder/testdata/hello` and `builder/testdata/escape` (a crate with
a `build.rs`, so a host program is compiled and linked too):

- writes under `CARGO_HOME`, `CARGO_TARGET_DIR` and `HOME`, which the
  builder already points into the build's folder;
- a temporary folder for the host linker: `cc` refuses with "Cannot
  create temporary file in /tmp/" when `TMPDIR` is outside the writable
  folder;
- no device write by cargo or rustc themselves; a build script that
  starts a program with `Stdio::null()` opens `/dev/null` for writing.

Landlock governs truncation only from ABI 3 (kernel 6.2) and scopes
signals only from ABI 6 (kernel 6.12). The node already negotiates the
ABI and handles every file right the kernel knows (`sandbox.rights`).

## Options

### A. Landlock write rights only beneath the build's folder

The same helper and the same split of `/` around the deny list, but
each grant outside the build's folder carries only `EXECUTE`,
`READ_FILE` and `READ_DIR`; the build's folder (split around any denied
path inside it, such as a `.env` in the source) and `/dev/null` carry
every right. The write rights are handled by the ruleset already, so
what is not granted is denied.

### B. A mount namespace: `/` read-only, the build's folder writable

The helper unshares a user and a mount namespace, remounts `/` read-only
and bind-mounts the build's folder writable (what bubblewrap does).
Strong and independent of Landlock's version, but it needs unprivileged
user namespaces, which Docker's default seccomp profile refuses and
Railway is not known to allow, and it is a second confinement mechanism
beside Landlock to build and test.

### C. Keep ADR-0039 as it is for builds

Builds keep the `host.exec` sandbox; the temporary `CARGO_HOME` and the
owner's press at install are the only guards. The toolchain, the cargo
configuration in `/tmp` and `/`, and the owner's files stay writable by
code nobody read.

For the escaped process:

### R1. The helper as a child subreaper

For a build the helper does not exec the command. It makes itself a
child subreaper (`PR_SET_CHILD_SUBREAPER`), starts the confined helper
that execs the command as its child, and when that child ends, or the
helper gets `SIGTERM`, kills its children until it has none: every
process the build leaves, `setsid` or not, becomes its child when its
own parent ends. It stays outside the build's Landlock domain, and the
domain scopes signals, so the build cannot kill it to get away.

### R2. A PID namespace

The command runs as the init of a new PID namespace; the kernel kills
the whole namespace when it ends. Sound, but it needs user namespaces,
as B does.

### R3. A known limit

Nothing kills a process that left the group; a backlog line records it.

## Comparison

| Criterion | A. Landlock writes | B. Mount namespace | C. As is |
|---|---|---|---|
| Toolchain and cargo configuration kept from the build | Yes | Yes | No |
| Works where user namespaces are refused (Docker, likely Railway) | Yes | No → no builds | Yes |
| New mechanism to build and test | A rights mask on the grants | Namespaces, mounts | None |
| Denied paths inside the build's folder stay denied | Yes (split) | Needs its own masking | Yes |
| Kernel needed | Landlock ABI 3 (6.2) | User namespaces | Landlock |

| Criterion | R1. Subreaper | R2. PID namespace | R3. Known limit |
|---|---|---|---|
| A `setsid` process ends with the build | Yes | Yes | No |
| The build cannot stop it from happening | From ABI 6 (signals scoped) | Yes | — |
| Works without user namespaces | Yes | No | Yes |
| Cost | One more process for the build's length, ~150 lines | Namespace setup | None |

## Decision

**A with R1, failing closed below Landlock ABI 3** (owner, 2026-11-09).
A keeps the one mechanism ADR-0039 already built and tested, and works
where B would leave the node with no builds at all. R1 is cheap and
needs no namespace.

- `sandbox.Sandbox.Confined(ctx, write, argv, dir, env…)` is `Command`
  with `write` the only writable folders (resolved to their real paths;
  a denied one is an error) and `/dev/null` writable; `host.exec` and
  skill scripts keep `Command`, unchanged.
- Below ABI 3 `Confined` returns `ErrCannotConfine` and the build is
  refused with that reason: truncating any readable file would stay
  open there.
- From ABI 6 a confined command's domain scopes signals as well as
  abstract sockets; `host.exec` keeps signals open (ADR-0039: `kill` must
  reach the owner's processes).
- The builder runs `cargo` (in `Build` and `Can`) through `Confined`
  with the build's temporary folder as the only writable tree, makes
  `cargo-home`, `target` and `tmp` in it before cargo runs, and sets
  `TMPDIR` to that `tmp`. A time limit sends the helper `SIGTERM`, not
  `SIGKILL`, so it can kill what the build left first; the group kill
  follows.
- The helper of a confined command turns its parent-death signal into
  `SIGTERM`, so the node's death reaches it as a signal it can act on.
- The built `.wasm` is read through `os.Root` on the build's folder: a
  `target` or `release` folder the build swapped for a symlink that
  leads out of it is refused.
- Landlock (to ABI 7 at least) does not govern a file's metadata, and a
  confined command may open for reading whatever it may read, a denied
  folder it may list (ADR-0057) included. The confined helper's seccomp
  filter therefore also refuses, with `EPERM`, inside the build's folder
  too, since seccomp cannot tell paths apart:
  - changing a file's owner or extended attributes, by path or through a
    descriptor (`chown`, `lchown` on amd64; `fchownat`, `fchown`,
    `setxattr`, `lsetxattr`, `fsetxattr`, `removexattr`, `lremovexattr`,
    `fremovexattr`, `setxattrat`, `removexattrat`);
  - changing a file's mode by path (`chmod` on amd64; `fchmodat`,
    `fchmodat2`);
  - `fchmod` with a mode that has a bit of `07022` (group or other write,
    setuid, setgid, sticky). cargo needs `fchmod`: it sets 0644 on every
    file it unpacks from crates.io, and refusing `fchmod` outright fails
    with "failed to set permissions to 644" (measured);
  - chattr's flags: `ioctl` with `FS_IOC_SETFLAGS`, `FS_IOC32_SETFLAGS` or
    `FS_IOC_FSSETXATTR`, and `file_setattr` (Linux 6.17), which sets them
    by path.

  The real builds of the tests (one with a build script compiled and
  linked, one with a dependency fetched and unpacked from crates.io) pass
  under the filter.
- The toolchain is pinned. The command line asks rustc, once at start and
  from `/`, which rustup toolchain it resolves to, and every build runs
  with `RUSTUP_TOOLCHAIN` set to it (`LocalConfig.Toolchain`); with none
  resolved the node builds nothing. A `rust-toolchain.toml` that names a
  toolchain by `path =`, in a parent or in the source, is then ignored by
  both proxies, `cargo` and the `rustc` cargo starts (measured: unpinned,
  the planted `cargo` ran; running the toolchain's own `cargo` was not
  enough, since it starts `rustc` through the proxy, and the planted
  `rustc` ran). The pin and the parents check guard against what lies
  outside the source. They do not reach the source's own
  `.cargo/config.toml`, which cargo reads in the work folder:
  `build.rustc`, `build.rustc-wrapper` or `target.<triple>.linker` there
  still choose what compiles the module. That is part of the source the
  owner reviews, and it runs in the same sandbox as the build script.
- Build folders are made in the node's own folder under the user's cache
  folder, `~/.cache/ganglion/builds` (`LocalConfig.TempRoot`, filled by
  `config`), made with mode 0700 and refused when it is anything else (a
  symlink, another owner, another mode); the janitor sweeps the leftovers
  in it. Before each build the root and every folder above it, up to `/`,
  must be a real folder that belongs to root or the node's user and that
  neither its group nor others may write; anything else refuses the build,
  naming the folder. Only root and the node's user can then put a file in
  a parent of a build. With no cache folder (no `HOME` or
  `XDG_CACHE_HOME`) the node builds nothing; it does not fall back to the
  shared temp folder. Not under the data folder, which the sandbox denies.
- Before each cargo run the builder also looks in the build's folder and
  every folder above it for `.cargo/config`, `.cargo/config.toml`,
  `rust-toolchain` and `rust-toolchain.toml`, which cargo and rustup's
  proxy read from every parent folder, and refuses the build naming the
  path when one is there (or when a path cannot be checked). With the
  parents only root's and the node user's, this check cannot be raced by
  another user. The owner's `~/.cargo/config.toml` is such a file, since
  home is a parent of the cache folder: a node whose user has one builds
  nothing until it is moved, which the refusal says.

## Security

- **Trust boundary:** the module's source and what it runs at build time
  (build script, procedural macros, the compiler's plug-ins) on one side;
  the owner's files, toolchain and processes and the node on the other.
- **What an attacker controls:** the source in the workspace (the agent
  in P3, or whoever got text into the agent's context), so every file
  operation, every process and every signal of the build.
- **Abuse cases and what stops them:**
  - Writes the toolchain, a `.cargo/config.toml` in a parent folder, or
    any file of the owner's: Landlock grants no write right outside the
    build's folder (tests `TestAConfinedCommandWritesOnlyItsFolder`,
    `TestABuildWritesOnlyItsFolderAndLeavesNoProcess`).
  - Changes the mode, owner or attributes of the node's database, a
    secret file or a denied folder: by path the seccomp filter refuses
    every such change (`TestAConfinedCommandChangesNoModeOrOwner`);
    through a descriptor, such as a denied folder opened for listing, it
    refuses every owner and attribute change, chattr's flags (by
    descriptor and by path), and a mode with group or other write,
    setuid, setgid or sticky
    (`TestAConfinedCommandGrantsNoModeThroughADescriptor`). Granting group
    and other read and execute stays open (known limits).
  - Plants a cargo or rustup configuration in a parent of the build's
    folder: only root and the node's user can write those folders (a
    root under a shared folder is refused,
    `TestARootUnderASharedFolderIsRefused`), and one found is refused
    with its path (`TestAConfigInAParentFolderRefusesTheBuild`).
  - Names another toolchain by path in a `rust-toolchain.toml`: builds
    are pinned to the node's toolchain
    (`TestABuildIsPinnedToTheNodesToolchain`).
  - Truncates a file on an old kernel: no confined command below ABI 3
    (`TestConfinedNeedsLandlock3`).
  - Reads a denied file, or a denied path inside its own folder: the deny
    list and the split hold as for `host.exec`
    (`TestAConfinedCommandMeetsTheDenyList`).
  - Detaches a process with `setsid` to outlive the build: the
    subreaper kills it when the build ends or is stopped
    (`TestAConfinedCommandLeavesNoProcess`,
    `TestAStoppedConfinedCommandLeavesNoProcess`, and the real build).
  - Kills the helper first, then detaches: signals are scoped from ABI 6
    (`TestAConfinedCommandCannotSignalOutside`). **Known limit:** on ABI
    3–5 a build can still kill its helper with `SIGKILL` and leave a
    process behind; that process can write only the build's folder,
    which is removed, and reads what `host.exec` reads. Backlog card
    G-127: a PID namespace where user namespaces exist, or refusing
    builds below ABI 6, once a node that builds runs there.
  - Swaps `target/…/release` for a symlink so the node reads a file of
    its own as the module: `os.Root` refuses a path that leaves the
    build's folder (`TestAWasmThatIsNotOneHonestFileIsRefused`).
- **Known limits, not covered:**
  - The network stays open to a build (cargo fetches crates; Landlock's
    network rules are not used), as it is to `host.exec`.
  - `fchmod` with a mode within `0755`: through a descriptor of any file or
    folder the build may open for reading, outside its folder too, a
    denied folder it may list included, this is allowed, because cargo
    sets 0644 and 0755 on what it unpacks. So a build can:
    - take mode bits away, e.g. the execute bit off the owner's
      toolchain, or the read bits off the owner's files;
    - grant group and other read and execute (measured: `fchmod 0755` of
      a 0700 denied folder, `fchmod 0644` of a 0600 file outside its
      folder), opening that file's contents, or that folder's names, to
      the machine's other local users. The build itself still cannot read
      a denied file.

    Only a mount namespace with the outside mounted read-only (option B)
    would close this.
  - A file's times: `utimensat`, `futimens`, and on amd64 `utime`,
    `utimes` and `futimesat` are not refused (cargo sets times for its
    fingerprints, and seccomp cannot tell paths apart), so a build can
    change the times of any file it can reach, outside its folder too.
  - A cargo or rustup configuration that root or the node's user (a
    pressed `host.exec`) puts in a parent of the build root stops builds
    until it is removed.

## Consequences

- A build cannot write, create or remove anything outside its folder, nor
  change a file's owner, attributes or chattr flags, nor grant write,
  setuid, setgid or sticky. It can still take mode bits away, grant group
  and other read and execute, and change times (above). So a build that needs to
  write elsewhere (a shared `sccache`, a global cargo cache) does not work;
  each build fetches its crates again into its temporary `CARGO_HOME`.
- A build script that sets a mode by path (`chmod` of a generated file),
  or grants group or other write through `fchmod`, fails, inside its own
  folder too.
- Builds need the node's user to have a cache folder, with no symlink and
  nothing writable by others from it up to `/`, and the toolchain to be a
  rustup toolchain; elsewhere the node builds nothing and says why.
- Builds need Landlock ABI 3 (Linux 6.2); a node on an older kernel has
  `host.exec` but no builder, and says why. Railway and a Pi are not yet
  measured (ADR-0039).
- A build's helper stays as a process for the length of the build, the
  one exception to "the helper execs the command"; it is still not a
  resident process (principle 8): it ends with the build.
- A build folder split by a denied path (a `.env` in the source) takes
  no new entries directly in it; the builder makes the folders cargo
  writes in beforehand.
- If the helper is killed by something outside the build (the OOM
  killer, the owner), what the build left is not reaped; the node's
  janitor removes files, not processes.

## Revisit when

- A node that builds runs on Landlock ABI 3–5 (close the known limit).
- A build needs to write outside its folder, or its network must be
  narrowed (Landlock ABI 4 network rules, MCP modules in P3).
- Builds move to a worker marked `builder` (P2): the same mode applies
  there.
- A node that builds shares its machine with other local users, for whom
  the read and execute bits a build can grant matter (option B, a mount
  namespace).
- A new kernel call changes a file's metadata (as `file_setattr` did in
  6.17): add it to the confined filter.
