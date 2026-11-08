# Command sandbox: what the hosts offer

- **Date:** 2026-11-08
- **Task:** P1 · M3d 3.20 ([plan](../plans/p1-m3d-sandbox.md), [ADR-0039](../adr/0039-command-sandbox.md))
- **Question:** does option A of ADR-0039 (`no_new_privs`, a clean environment, Landlock, the node not
  dumpable; no PID namespace, owner 2026-11-08) hold on the owner's machine, in a container, on Railway
  and on a Pi; and what does finding `.env` files cost?
- **Status:** the owner's machine and a local Docker container measured; **Railway and the Pi wait for
  the owner's runs**.

## Method

`tools/sandboxprobe` prints what a host offers: the kernel, Landlock's ABI (or why there is none),
whether `no_new_privs` can be set, whether a process that clears its dumpable flag gets a
`/proc/self/environ` owned by root, the seccomp mode, Yama, user namespaces, container marks, and how
long a search for `.env` files under the home folder takes (depth 4, skipping `.git`, `node_modules`,
`.cache`, `go`, `.cargo`, `.rustup`). It changes nothing on the host.

- Owner's machine: `go run ./tools/sandboxprobe` as the owner's user.
- Docker: `docker run --rm --entrypoint /probe sandboxprobe` from `tools/sandboxprobe/Dockerfile`
  (distroless, `USER nonroot`), Docker's default seccomp profile; again with `--user 0`.
- Railway: the same image as a service.
- Pi: `GOARCH=arm64` build, run as the node's user.

## Results

### Owner's machine (Manjaro, amd64)

| Check | Value |
|---|---|
| kernel | 6.18.55-1-MANJARO |
| euid | 1000 |
| Landlock ABI | 7 |
| no_new_privs | set, reads 1 |
| not dumpable closes environ | yes, owned by root |
| seccomp mode | 0 |
| Yama ptrace_scope | 1 |
| unprivileged user namespaces | 1, max 126831 |
| container | none (pid 1 cgroup `0::/init.scope`) |
| .env files under $HOME, depth 4 | 3 found in 2,471 folders, 111 ms |

The real node on this machine logs `sandbox: ready` with 12 denied paths; its self-test ran the
helper through `ganglion __sandbox` and could not read the denied file. The package's abuse tests pass
here: a denied file, a write into a denied folder, a symlink to a denied file and a file created after
the grants all fail; `env` shows the allowlist only; `sudo -n true` refuses under `no_new_privs`.

### Local Docker (same kernel, default seccomp profile)

| Check | Value |
|---|---|
| euid | 65532 (`nonroot`); 0 with `--user 0` |
| Landlock ABI | 7 (the default seccomp profile lets the syscalls through) |
| no_new_privs | set, reads 1 |
| not dumpable closes environ | yes, owned by root (as root: meaningless) |
| seccomp mode | 2 (filter) |
| container | `/.dockerenv`; pid 1 cgroup `0::/` |

### Railway

Waiting for the owner's run.

### Raspberry Pi (arm64)

Waiting for the owner's run.

## Conclusion (so far)

1. **Landlock** is there on the owner's machine and inside a container with Docker's default seccomp
   profile. A host whose kernel or seccomp profile lacks it gets commands off with "the kernel has no
   Landlock" (fail closed).
2. **Not dumpable closes the node's environment** for a process of the same user, in and out of a
   container. As root it means nothing, and the sandbox is off there anyway.
3. **Root:** an image without `USER` runs as root, and commands are off; the M4 image (task 4.6) sets
   `USER`.
4. **`.env` files:** a search to depth 4 under a developer's home took 111 ms over 2,471 folders. The
   node searches the home folder and the command's folder to depth 3 at each command, skipping the
   same tool folders; a `.env` deeper than that stays readable. That is a known gap of denying by path
   when Landlock cannot deny by name; the class table (task 3.22) can add a check of a command's
   arguments for such names.
