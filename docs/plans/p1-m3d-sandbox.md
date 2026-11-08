# P1 · M3d task 3.20: the command sandbox — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** a program the node starts for a command or a skill script cannot gain privileges, gets a
clean environment, cannot read the node's environment or memory, and can neither read nor write the
node's data and secret files or the owner's secret stores; where that cannot be set up, the node says
so and offers no commands.

**Architecture:** a leaf package `sandbox`. The node computes, per command, which paths a command may
reach: everything under `/` except the denied paths, as a list of Landlock grants found by splitting
each denied path's ancestors into their other entries. It then starts its own binary as a helper
(`ganglion __sandbox`) with a clean environment and that list on file descriptor 3. The helper sets
`no_new_privs`, restricts itself with Landlock and `exec`s the command. The node's environment and
memory are closed by the node itself being not dumpable (`cmd/harden_linux.go`, already there), so no
PID namespace is used and `ps` still sees the machine (owner, 2026-11-08). At start the node checks it
is not root, Landlock is there, it is not dumpable, and a self-test cannot read a denied file; any
failure turns the sandbox off with a reason that `/status` shows.

**Tech stack:** Go 1.27, raw Landlock syscalls through `golang.org/x/sys/unix` (already in the module
graph as an indirect dependency; it becomes direct, no new module). No cgo.

**Spec:** R-CMD-3 (as changed on 2026-11-08), [M3d design](p1-m3d-host-commands-design.md) section 5,
[ADR-0039](../adr/0039-command-sandbox.md) (Proposed; option A without the PID namespace). Read them
with this plan. Tasks 3.21–3.27 get their own plan after this one, on what the spike found.

## Global Constraints

- All documentation, comments and doc comments are in English; [conventions](../conventions.md) apply.
- Architecture docs change in the same commit as the code they describe (`05-code-map.md`, its
  dependency diagram included).
- `sandbox` imports nothing of ganglion but `status`, `texts`; domain packages never import
  `samber/do`. Only `config` builds the sandbox.
- Linux only: Linux code in `_linux.go` files; every other OS builds and reports `NotLinux`.
- Fail closed: any doubt (an error opening a grant, a listing that fails, a self-test that reads the
  denied file) means the command does not run or the sandbox is off; never a run without the rules.
- A reason is a `texts` key, never an error's text. Every new key has English, Russian and Ukrainian
  texts with the same placeholders (`TestShippedFilesMatchEnglish`); no markup marks (`*_` `` ` ``
  `~[]<>#|`) in a template.
- An abuse test goes with every guarantee (conventions, section 11).
- Commit only on branch `m3d-sandbox`, never on `master`. A full chatlab run is never automatic.

## Review Focus

1. **A symlink to a denied path** reaches nothing: splitting never grants a symlink (task 3, task 4).
2. **A file created later** under a split parent is denied, not allowed (task 3): fail closed.
3. **The helper never runs the command without its rules:** any error before `exec` exits non-zero
   (task 4).
4. **`no_new_privs` and the Landlock domain are set on the thread that calls `exec`** (task 4).
5. **The self-test is real:** it runs the helper and reads a denied file, so a kernel that accepts
   the syscalls but enforces nothing turns the sandbox off (task 5).
6. **`env` inside the sandbox** shows only the allowlist (task 4).

## Files

| File | Responsibility |
|---|---|
| `tools/sandboxprobe/main.go` (new) | the spike: what a host offers (task 1) |
| `tools/sandboxprobe/Dockerfile` (new) | the probe as a container for Railway (task 1) |
| `docs/research/2026-11-08-command-sandbox-spike.md` (new) | what the spike found on each host (task 2) |
| `sandbox/grants.go` (new) | `Grant`, `Grants`: the split of `/` around the denied paths; pure over a listing |
| `sandbox/deny.go` (new) | `NodeFiles`, `DenyList`, `HomeSecrets`, `EnvFiles`: what is denied |
| `sandbox/sandbox.go` (new) | `Reason`, `Policy`, `Sandbox`, `New`, `Command`, `HelperArg` |
| `sandbox/landlock_linux.go` (new) | the Landlock syscalls, `abi`, `rights`, `restrict` |
| `sandbox/helper_linux.go` (new) | `Helper`: read the spec, `no_new_privs`, Landlock, `exec` |
| `sandbox/kernel_linux.go`, `sandbox/kernel_other.go` (new) | `Kernel`: root, Landlock's ABI, dumpable; the non-Linux stub |
| `sandbox/status.go` (new) | the `/status` line: ready or off with its reason |
| `cmd/sandbox.go` (new), `cmd/root.go` | the hidden `__sandbox` command |
| `tools/chatlab/main.go` | the chatlab node answers `__sandbox` too |
| `config/sandbox.go` (new), `config/status.go`, `config/bootstrap.go` | the provider, the source, `SandboxDeny` |
| `cmd/flags/flags.go`, `cmd/run.go` | `--sandbox-deny` / `GANGLION_SANDBOX_DENY` |
| `texts/texts.go`, `texts/lang/{en,ru,uk}.yaml` | the sandbox's `status.*` keys |

---

### Task 1: The spike probe

What each host offers decides whether option A holds there (ADR-0039, "Revisit when"). The probe
reports it without the sandbox package, so it runs before any of it exists.

**Files:**
- Create: `tools/sandboxprobe/main.go`, `tools/sandboxprobe/Dockerfile`

**Interfaces:**
- Produces: a binary that prints one `| name | value |` Markdown row per check, ready to paste into
  the research note; `-hold` waits for a signal after the report.

- [x] **Step 1: Write the probe**

```go
// Command sandboxprobe reports what a host offers the command sandbox of
// ADR-0039: Landlock, no_new_privs, a not-dumpable process, the container it
// runs in. It is the spike of M3d task 3.20 and changes nothing on the host.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func main() {
	hold := flag.Bool("hold", false, "wait for a signal after the report, for a platform that restarts a container that exits")
	flag.Parse()
	fmt.Println("| Check | Value |")
	fmt.Println("|---|---|")
	for _, c := range checks() {
		fmt.Printf("| %s | %s |\n", c[0], c[1])
	}
	if *hold {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		<-ctx.Done()
	}
}

func checks() [][2]string {
	return [][2]string{
		{"kernel", kernel()},
		{"arch", runtime.GOARCH},
		{"euid", strconv.Itoa(os.Geteuid())},
		{"Landlock ABI", landlock()},
		{"no_new_privs", nnp()},
		{"not dumpable closes environ", dumpable()},
		{"seccomp mode", statusField("Seccomp")},
		{"Yama ptrace_scope", read("/proc/sys/kernel/yama/ptrace_scope")},
		{"unprivileged user namespaces", read("/proc/sys/kernel/unprivileged_userns_clone") + ", max " + read("/proc/sys/user/max_user_namespaces")},
		{"container", container()},
		{".env files under $HOME, depth 4", envScan()},
	}
}

func kernel() string {
	var u unix.Utsname
	if err := unix.Uname(&u); err != nil {
		return "error: " + err.Error()
	}
	return unix.ByteSliceToString(u.Release[:]) // gVisor reports 4.4.0
}

func landlock() string {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	switch {
	case errno == unix.ENOSYS:
		return "none: no syscall (an old kernel, or seccomp blocks it)"
	case errno == unix.EOPNOTSUPP:
		return "built but off (not in the lsm= list)"
	case errno != 0:
		return "error: " + errno.Error()
	}
	return strconv.Itoa(int(v))
}

func nnp() string {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return "cannot set: " + err.Error()
	}
	v, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil {
		return "cannot read: " + err.Error()
	}
	return "set, reads " + strconv.Itoa(v)
}

func dumpable() string {
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return "cannot set: " + err.Error()
	}
	var st unix.Stat_t
	if err := unix.Stat("/proc/self/environ", &st); err != nil {
		return "error: " + err.Error()
	}
	if os.Geteuid() == 0 {
		return "meaningless as root (owner uid " + strconv.Itoa(int(st.Uid)) + ")"
	}
	if st.Uid == 0 {
		return "yes, owned by root"
	}
	return "no, owned by uid " + strconv.Itoa(int(st.Uid))
}

func statusField(name string) string {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "error: " + err.Error()
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, name+":"); ok {
			return strings.TrimSpace(v) // 0 off, 1 strict, 2 filter
		}
	}
	return "absent"
}

func read(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "absent"
	}
	return strings.TrimSpace(string(b))
}

func container() string {
	var marks []string
	for _, f := range []string{"/.dockerenv", "/run/.containerenv"} {
		if _, err := os.Stat(f); err == nil {
			marks = append(marks, f)
		}
	}
	if b, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		marks = append(marks, "pid 1 cgroup "+strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0]))
	}
	if len(marks) == 0 {
		return "no marks"
	}
	return strings.Join(marks, "; ")
}

// envScan measures what finding .env files would cost: R-CMD-3 denies them
// in any folder, and Landlock cannot deny by name.
func envScan() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "no home"
	}
	start, found, dirs := time.Now(), 0, 0
	skip := map[string]bool{".git": true, "node_modules": true, ".cache": true, "go": true, ".cargo": true, ".rustup": true}
	err = filepath.WalkDir(home, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable folder is skipped, not fatal
		}
		depth := strings.Count(strings.TrimPrefix(p, home), string(filepath.Separator))
		if d.IsDir() {
			dirs++
			if skip[d.Name()] || depth >= 4 {
				return filepath.SkipDir
			}
			return nil
		}
		if n := d.Name(); n == ".env" || strings.HasPrefix(n, ".env.") {
			found++
		}
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return "no home"
	}
	return fmt.Sprintf("%d found in %d folders, %s", found, dirs, time.Since(start).Round(time.Millisecond))
}
```

- [x] **Step 2: Write the Dockerfile**

```dockerfile
# The sandbox probe as a container, for Railway: M3d task 3.20.
# docker build -f tools/sandboxprobe/Dockerfile -t sandboxprobe .
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /probe ./tools/sandboxprobe

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /probe /probe
ENTRYPOINT ["/probe", "-hold"]
```

- [x] **Step 3: Build, lint and run it here**

Run: `go mod tidy && go build ./tools/sandboxprobe && go run ./tools/sandboxprobe`
Expected: a table; on the owner's machine Landlock ABI ≥ 5, `no_new_privs` "set, reads 1",
"not dumpable closes environ" "yes, owned by root". `go mod tidy` moves `golang.org/x/sys` to the
direct requirements. Then `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./tools/sandboxprobe/...`
Expected: 0 issues.

- [x] **Step 4: Run it in a local container** (Docker's default seccomp profile, as most hosts)

Run: `docker build -f tools/sandboxprobe/Dockerfile -t sandboxprobe . && docker run --rm --entrypoint /probe sandboxprobe`
Expected: a table; record it. (The image's `ENTRYPOINT` holds with `-hold`; `--entrypoint /probe`
runs it without, so `docker run` returns.) Run it again with `--user 0` to see root. If `docker` is
not installed, skip and say so in the note.

- [x] **Step 5: Commit**

```bash
git add tools/sandboxprobe go.mod go.sum
git commit -m "tools: sandboxprobe, the M3d sandbox spike"
```

### Task 2: The spike on the hosts, and the decision

**Files:**
- Create: `docs/research/2026-11-08-command-sandbox-spike.md`
- Modify: `docs/adr/0039-command-sandbox.md` (only if a host changes the decision)

- [ ] **Step 1: Ask the owner to run the probe on Railway and the Pi**, with the exact commands:
  - Pi: `GOOS=linux GOARCH=arm64 go build -o bin/sandboxprobe-arm64 ./tools/sandboxprobe`, copy it to
    the Pi, run `./sandboxprobe-arm64` as the user the node would run as;
  - Railway: a new service from this repository with the Dockerfile path
    `tools/sandboxprobe/Dockerfile` (or `railway up` with that Dockerfile); the table is in the
    deploy log; delete the service after.

  The owner pastes both tables. Tasks 3 and 4 may go on meanwhile: they hold on the owner's machine.

- [ ] **Step 2: Write the research note** in the format of the other notes in `docs/research/`
  (question, method, results, conclusion). Results: one table per host (owner's machine, local
  Docker, Railway, Pi). Conclusion answers:
  1. Where Landlock is present, and so where commands can be on.
  2. Whether "not dumpable closes environ" holds everywhere the node runs as a non-root user.
  3. Whether Railway runs the image's `USER nonroot`, or root (then commands are off there until the
     M4 image, task 4.6, sets `USER`).
  4. What finding `.env` files costs (`.env files under $HOME, depth 4`), and so whether the sandbox
     denies them by a scan at each command (task 3's `EnvFiles`) and where the scan stops.

- [ ] **Step 3: Checkpoint with the owner.** If a host lacks Landlock: commands are off there (the
  ADR's "Revisit when"); option B is not taken without the owner. If the scan of `.env` files is too
  slow on the owner's machine (over 100 ms), propose a lower depth to the owner before task 3's
  `EnvFiles` is written.

- [ ] **Step 4: Commit**

```bash
git add docs/research/2026-11-08-command-sandbox-spike.md docs/adr/0039-command-sandbox.md
git commit -m "docs: the M3d sandbox spike on the owner's machine, Docker, Railway and a Pi"
```

### Task 3: What is denied, and the grants around it

Pure code: no syscall, so every rule of the split is a unit test.

**Files:**
- Create: `sandbox/grants.go`, `sandbox/deny.go`, `sandbox/grants_test.go`, `sandbox/deny_test.go`

**Interfaces:**
- Produces:
  - `type Grant struct { Path string; Dir bool }`
  - `type Lister func(dir string) ([]fs.DirEntry, error)`
  - `func Grants(deny []string, list Lister) ([]Grant, error)`
  - `type NodeFiles struct { DB, LogDir, ModulesDir, Workspace, Languages string }`
  - `func HomeSecrets(home string) []string`
  - `func DenyList(home string, node NodeFiles, environ, extra []string) []string`
  - `func EnvFiles(roots []string, depth int, list Lister) []string`

- [x] **Step 1: Write the failing tests of the split**

```go
package sandbox

import (
	"io/fs"
	"slices"
	"testing"
	"testing/fstest"
)

// tree is a listing over an in-memory file system rooted at "/".
func tree(t *testing.T, m fstest.MapFS) Lister {
	t.Helper()
	return func(dir string) ([]fs.DirEntry, error) {
		name := strings.TrimPrefix(dir, "/")
		if name == "" {
			name = "."
		}
		return fs.ReadDir(m, name)
	}
}

func paths(gs []Grant) []string {
	out := make([]string, 0, len(gs))
	for _, g := range gs {
		out = append(out, g.Path)
	}
	slices.Sort(out)
	return out
}

func TestGrantsWithoutDenyIsTheRoot(t *testing.T) {
	got, err := Grants(nil, tree(t, fstest.MapFS{"etc/hosts": {}}))
	if err != nil {
		t.Fatal(err)
	}
	if want := []Grant{{Path: "/", Dir: true}}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestGrantsSplitAroundADeniedFolder(t *testing.T) {
	m := fstest.MapFS{
		"etc/hosts": {}, "home/u/.ssh/id": {}, "home/u/code/a.go": {}, "home/u/notes.txt": {},
		"home/v/x": {},
	}
	got, err := Grants([]string{"/home/u/.ssh"}, tree(t, m))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/etc", "/home/u/code", "/home/u/notes.txt", "/home/v"}
	if !slices.Equal(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
	for _, g := range got {
		if g.Path == "/home/u/notes.txt" && g.Dir {
			t.Fatal("a file granted as a folder")
		}
	}
}

func TestGrantsNeverGrantASymlink(t *testing.T) {
	m := fstest.MapFS{
		"home/u/.ssh/id": {},
		"home/u/keys":    {Mode: fs.ModeSymlink, Data: []byte("/home/u/.ssh")},
		"home/u/a.txt":   {},
	}
	got, err := Grants([]string{"/home/u/.ssh"}, tree(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(paths(got), "/home/u/keys") {
		t.Fatal("a symlink next to a denied path was granted; it would grant its target")
	}
}

func TestGrantsDenyAFile(t *testing.T) {
	m := fstest.MapFS{"srv/app/ganglion.db": {}, "srv/app/ganglion": {}}
	got, err := Grants([]string{"/srv/app/ganglion.db"}, tree(t, m))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/srv/app/ganglion"}; !slices.Equal(paths(got), want) {
		t.Fatalf("got %v, want %v", paths(got), want)
	}
}

func TestGrantsFailOnAnUnlistableAncestor(t *testing.T) {
	list := func(string) ([]fs.DirEntry, error) { return nil, fs.ErrPermission }
	if _, err := Grants([]string{"/home/u/.ssh"}, list); err == nil {
		t.Fatal("an ancestor that cannot be listed must fail the command, not grant it whole")
	}
}
```

Add `"strings"` to the test's imports.

- [x] **Step 2: Run them to see them fail**

Run: `go test ./sandbox/ -run TestGrants`
Expected: FAIL, `undefined: Grants`.

- [x] **Step 3: Write the split**

```go
// Package sandbox runs host commands and skill scripts so that they cannot
// gain privileges, see the node's variables, or read or write the node's data
// and secret files and the owner's secret stores (R-CMD-3, ADR-0039).
package sandbox

import (
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
)

// Grant is a path a command may reach, with everything beneath it when Dir.
type Grant struct {
	Path string
	Dir  bool
}

// Lister lists a folder's entries without following symlinks (os.ReadDir).
type Lister func(dir string) ([]fs.DirEntry, error)

// Grants is what Landlock grants so that a command reaches everything under
// "/" but deny: Landlock only grants whole trees, so each denied path's
// ancestors are split into their other entries. A symlink among them is
// never granted, since granting it would grant its target; what it points
// to is reached, or not, by its own path. Something created later directly
// in a split folder is denied: the grants were listed before it existed.
// deny holds clean absolute paths with no symlink in them (DenyList).
func Grants(deny []string, list Lister) ([]Grant, error) {
	return split("/", deny, list)
}

func split(dir string, deny []string, list Lister) ([]Grant, error) {
	if slices.Contains(deny, dir) {
		return nil, nil
	}
	if !slices.ContainsFunc(deny, func(d string) bool { return under(d, dir) }) {
		return []Grant{{Path: dir, Dir: true}}, nil
	}
	entries, err := list(dir)
	if err != nil {
		return nil, fmt.Errorf("sandbox: list %s: %w", dir, err)
	}
	var out []Grant
	for _, e := range entries {
		p := path.Join(dir, e.Name())
		switch {
		case e.Type()&fs.ModeSymlink != 0:
		case e.IsDir():
			gs, err := split(p, deny, list)
			if err != nil {
				return nil, err
			}
			out = append(out, gs...)
		case !slices.Contains(deny, p):
			out = append(out, Grant{Path: p})
		}
	}
	return out, nil
}

// under reports whether p lies strictly beneath dir.
func under(p, dir string) bool {
	if dir == "/" {
		return p != "/"
	}
	return strings.HasPrefix(p, dir+"/")
}
```

- [x] **Step 4: Run the split's tests**

Run: `go test ./sandbox/ -run TestGrants`
Expected: PASS.

- [x] **Step 5: Write the failing tests of the deny list**

```go
package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestDenyListHoldsTheNodesFilesAndSecrets(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir()) // DenyList answers with real paths
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(dir, "home")
	for _, p := range []string{"home/.ssh", "data/logs", "data/modules", "ws", "keys"} {
		if err := os.MkdirAll(filepath.Join(dir, p), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"data/ganglion.db", "data/ganglion.db-wal", "keys/bot", "home/.netrc"} {
		if err := os.WriteFile(filepath.Join(dir, f), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	node := NodeFiles{DB: filepath.Join(dir, "data/ganglion.db"), LogDir: filepath.Join(dir, "data/logs"),
		ModulesDir: filepath.Join(dir, "data/modules"), Workspace: filepath.Join(dir, "ws")}
	environ := []string{"PATH=/bin", "TELEGRAM_BOT_TOKEN_FILE=" + filepath.Join(dir, "keys/bot"), "X_FILE="}
	got := DenyList(home, node, environ, []string{filepath.Join(dir, "missing")})
	for _, want := range []string{"data/ganglion.db", "data/ganglion.db-wal", "data/logs", "data/modules", "ws",
		"keys/bot", "home/.ssh", "home/.netrc"} {
		if !slices.Contains(got, filepath.Join(dir, want)) {
			t.Errorf("%s is not denied; got %v", want, got)
		}
	}
	for _, p := range got {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s does not exist and should be left out", p)
		}
	}
}

func TestDenyListResolvesSymlinks(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(dir, "real")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	got := DenyList(dir, NodeFiles{}, nil, []string{filepath.Join(dir, "link")})
	if !slices.Contains(got, real) {
		t.Fatalf("got %v, want the symlink's target %s", got, real)
	}
}

func TestEnvFilesFindsThemToADepth(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"a/.env", "a/.env.local", "a/b/c/d/e/.env", "a/node_modules/x/.env", "a/env"} {
		p := filepath.Join(dir, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := EnvFiles([]string{dir}, 3, os.ReadDir)
	want := []string{filepath.Join(dir, "a/.env"), filepath.Join(dir, "a/.env.local")}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}
```

- [x] **Step 6: Run them to see them fail**

Run: `go test ./sandbox/ -run 'TestDenyList|TestEnvFiles'`
Expected: FAIL, `undefined: DenyList`.

- [x] **Step 7: Write the deny list** (use the depth task 2's checkpoint settled; 3 below)

```go
package sandbox

import (
	"io/fs"
	"path/filepath"
	"slices"
	"strings"
)

// homeSecrets are the owner's secret stores under the home folder that every
// command is denied (R-CMD-3); the owner adds more with --sandbox-deny.
var homeSecrets = []string{
	".ssh", ".gnupg", ".netrc", ".git-credentials", ".pgpass", ".npmrc", ".pypirc",
	".aws", ".azure", ".kube", ".docker/config.json", ".password-store", ".local/share/keyrings",
	".config/gcloud", ".config/gh", ".config/scw", ".config/railway", ".railway",
	".claude/.credentials.json",
}

// skipped are folders a search for .env files does not enter: big, and
// not where an owner keeps one.
var skipped = []string{".git", "node_modules", ".cache", ".cargo", ".rustup", "go"}

// NodeFiles are the node's own files: its data, logs, modules and workspace.
type NodeFiles struct{ DB, LogDir, ModulesDir, Workspace, Languages string }

// HomeSecrets is the default list of secret stores under home.
func HomeSecrets(home string) []string {
	out := make([]string, 0, len(homeSecrets))
	for _, p := range homeSecrets {
		out = append(out, filepath.Join(home, p))
	}
	return out
}

// DenyList is every path a command may neither read nor write: the node's
// files (the database with its journal files), each <NAME>_FILE secret in
// environ, the owner's secret stores under home, and extra. Each is made
// absolute with its symlinks resolved, since Landlock sees real paths; one
// that does not exist is left out, and is reached by nothing a later split
// grants unless it is created under a granted folder (see Grants).
func DenyList(home string, node NodeFiles, environ, extra []string) []string {
	var raw []string
	if node.DB != "" {
		raw = append(raw, node.DB, node.DB+"-wal", node.DB+"-shm", node.DB+"-journal")
	}
	raw = append(raw, node.LogDir, node.ModulesDir, node.Workspace, node.Languages)
	for _, kv := range environ {
		if name, v, ok := strings.Cut(kv, "="); ok && strings.HasSuffix(name, "_FILE") && v != "" {
			raw = append(raw, v)
		}
	}
	raw = append(append(raw, HomeSecrets(home)...), extra...)
	var out []string
	for _, p := range raw {
		if p == "" {
			continue
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil && !slices.Contains(out, real) {
			out = append(out, real)
		}
	}
	return out
}

// EnvFiles finds .env and .env.* files under roots down to depth folders,
// skipping big tool folders: Landlock cannot deny by name, so they are
// denied by path, found at each command (R-CMD-3). A deeper one stays
// readable; the research note of the spike says why the depth is enough.
func EnvFiles(roots []string, depth int, list Lister) []string {
	var out []string
	var walk func(dir string, left int)
	walk = func(dir string, left int) {
		entries, err := list(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			p := filepath.Join(dir, e.Name())
			switch n := e.Name(); {
			case e.Type()&fs.ModeSymlink != 0:
			case e.IsDir():
				if left > 1 && !slices.Contains(skipped, n) {
					walk(p, left-1)
				}
			case n == ".env" || strings.HasPrefix(n, ".env."):
				out = append(out, p)
			}
		}
	}
	for _, r := range roots {
		walk(r, depth)
	}
	return out
}
```

Depth counts folders: `walk(root, 3)` reads the root and two levels below it. Adjust the test's
expectation if task 2 settled another depth.

- [x] **Step 8: Run the package's tests and lint**

Run: `go test -race ./sandbox/ && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./sandbox/...`
Expected: PASS; 0 issues.

- [x] **Step 9: Commit**

```bash
git add sandbox
git commit -m "sandbox: what a command is denied, and the Landlock grants around it"
```

### Task 4: The helper: `no_new_privs`, Landlock, `exec`

**Files:**
- Create: `sandbox/landlock_linux.go`, `sandbox/helper_linux.go`, `sandbox/helper_test.go` (linux),
  `sandbox/main_test.go`
- Modify: `sandbox/sandbox.go` (new here: `HelperArg`, `spec`, `Command`)

**Interfaces:**
- Consumes: `Grant`, `Grants`, `Lister`, `EnvFiles` (task 3).
- Produces:
  - `const HelperArg = "__sandbox"`: the argument that makes the binary the helper.
  - `func Helper() int` (linux): run as `<binary> __sandbox` with the spec on fd 3; returns only on
    failure, with `exitSetup` (125).
  - `func command(ctx context.Context, self string, sp spec) (*exec.Cmd, error)`: the unexported
    builder `Sandbox.Command` (task 5) uses.
  - `type spec struct { Argv, Env []string; Dir string; Grants []Grant; ABI int; Try string }`

- [ ] **Step 1: Write the test harness and the failing abuse tests**

`sandbox/main_test.go`:

```go
package sandbox

import (
	"os"
	"testing"
)

// TestMain lets the test binary be the helper, as the ganglion binary is.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == HelperArg {
		os.Exit(Helper())
	}
	os.Exit(m.Run())
}
```

`sandbox/helper_test.go`:

```go
//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run runs argv in the sandbox with deny denied, and returns its output and
// exit code. It skips where the kernel has no Landlock.
func run(t *testing.T, deny []string, argv ...string) (string, int) {
	t.Helper()
	abi, err := landlockABI()
	if err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	grants, err := Grants(deny, os.ReadDir)
	if err != nil {
		t.Fatal(err)
	}
	sp := spec{Argv: argv, Env: []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}, Dir: "/", Grants: grants, ABI: abi}
	cmd, err := command(context.Background(), self, sp)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &exit):
		return out.String(), exit.ExitCode()
	}
	t.Fatal(err)
	return "", 0
}

func secretDir(t *testing.T) (dir, file string) {
	t.Helper()
	dir = t.TempDir()
	file = filepath.Join(dir, "ganglion.db")
	if err := os.WriteFile(file, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return real, filepath.Join(real, "ganglion.db")
}

func TestADeniedFileCannotBeRead(t *testing.T) {
	dir, file := secretDir(t)
	out, code := run(t, []string{dir}, "cat", file)
	if code == 0 || strings.Contains(out, "hunter2") {
		t.Fatalf("read a denied file: exit %d, %q", code, out)
	}
}

func TestADeniedFolderCannotBeWritten(t *testing.T) {
	dir, _ := secretDir(t)
	if _, code := run(t, []string{dir}, "touch", filepath.Join(dir, "new")); code == 0 {
		t.Fatal("created a file in a denied folder")
	}
}

func TestASymlinkDoesNotReachADeniedFile(t *testing.T) {
	dir, file := secretDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if out, code := run(t, []string{dir}, "cat", link); code == 0 || strings.Contains(out, "hunter2") {
		t.Fatalf("read a denied file through a symlink: exit %d, %q", code, out)
	}
}

func TestAnAllowedFileReads(t *testing.T) {
	if _, err := os.Stat("/etc/hostname"); err != nil {
		t.Skip("no /etc/hostname")
	}
	dir, _ := secretDir(t)
	out, code := run(t, []string{dir}, "cat", "/etc/hostname")
	if code != 0 {
		t.Fatalf("an allowed file did not read: exit %d, %q", code, out)
	}
}

// TestAFileCreatedAfterTheGrantsIsDenied pins fail closed: a split folder's
// grants are its entries at the time, so a newcomer is reached by nothing.
func TestAFileCreatedAfterTheGrantsIsDenied(t *testing.T) {
	abi, err := landlockABI()
	if err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	dir, _ := secretDir(t)
	inner := filepath.Join(dir, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	grants, err := Grants([]string{filepath.Join(inner, "denied")}, os.ReadDir) // splits inner
	if err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(inner, "late")
	if err := os.WriteFile(late, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := command(t.Context(), self, spec{Argv: []string{"cat", late}, Env: []string{"PATH=/usr/bin:/bin"},
		Dir: "/", Grants: grants, ABI: abi})
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(out), "hunter2") {
		t.Fatalf("a file created after the grants was read: %v, %q", err, out)
	}
}

func TestNoNewPrivsIsSet(t *testing.T) {
	out, code := run(t, nil, "grep", "NoNewPrivs", "/proc/self/status")
	if code != 0 || !strings.Contains(out, "1") {
		t.Fatalf("no_new_privs is not set: exit %d, %q", code, out)
	}
}

func TestTheEnvironmentIsTheAllowlist(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "hunter2")
	out, code := run(t, nil, "env")
	if code != 0 || strings.Contains(out, "hunter2") || strings.Contains(out, "TELEGRAM") {
		t.Fatalf("the node's environment leaked: exit %d, %q", code, out)
	}
	if !strings.Contains(out, "LANG=C.UTF-8") {
		t.Fatalf("the allowlist is missing: %q", out)
	}
}

func TestSudoGainsNothing(t *testing.T) {
	if _, err := exec.LookPath("sudo"); err != nil {
		t.Skip("no sudo here")
	}
	out, code := run(t, nil, "sudo", "-n", "true")
	if code == 0 {
		t.Fatalf("sudo ran: %q", out)
	}
}

func TestAMissingProgramFailsBeforeRunning(t *testing.T) {
	if _, code := run(t, nil, "no-such-program-ganglion"); code != exitSetup {
		t.Fatalf("exit %d, want %d", code, exitSetup)
	}
}
```

Add `"errors"` to the imports.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./sandbox/ -run 'TestADenied|TestASymlink|TestAnAllowed|TestNoNewPrivs|TestTheEnvironment|TestSudo|TestAMissing'`
Expected: FAIL, `undefined: Helper` / `undefined: command`.

- [ ] **Step 3: Write the Landlock syscalls**

```go
//go:build linux

package sandbox

import (
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/unix"
)

// fileRights are the rights Landlock accepts on a file rather than a folder.
const fileRights = unix.LANDLOCK_ACCESS_FS_EXECUTE | unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_TRUNCATE | unix.LANDLOCK_ACCESS_FS_IOCTL_DEV

// landlockABI is the kernel's Landlock version; an error when it has none.
func landlockABI() (int, error) {
	v, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, unix.LANDLOCK_CREATE_RULESET_VERSION)
	if errno != 0 {
		return 0, errno
	}
	return int(v), nil
}

// rights is every file system right ABI abi knows, up to version 5. Unix
// socket connections (a later ABI) are not handled: Docker's socket is the
// class table's to forbid (ADR-0039).
func rights(abi int) uint64 {
	r := uint64(unix.LANDLOCK_ACCESS_FS_MAKE_SYM<<1 - 1) // version 1: EXECUTE … MAKE_SYM
	if abi >= 2 {
		r |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		r |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	if abi >= 5 {
		r |= unix.LANDLOCK_ACCESS_FS_IOCTL_DEV
	}
	return r
}

// rulesetAttr is the part of struct landlock_ruleset_attr this package
// sets; the kernel accepts the shorter size.
type rulesetAttr struct{ handledAccessFS uint64 }

// restrict confines the calling thread, and what it execs, to grants.
func restrict(abi int, grants []Grant) error {
	handled := rights(abi)
	attr := rulesetAttr{handledAccessFS: handled}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	if errno != 0 {
		return fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	defer func() { _ = unix.Close(int(fd)) }()
	for _, g := range grants {
		if err := addRule(int(fd), g, handled); err != nil {
			return err
		}
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, fd, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

func addRule(ruleset int, g Grant, handled uint64) error {
	p, err := unix.Open(g.Path, unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil // gone since it was listed: nothing to grant
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", g.Path, err)
	}
	defer func() { _ = unix.Close(p) }()
	access := handled
	if !g.Dir {
		access &= fileRights
	}
	attr := unix.LandlockPathBeneathAttr{Allowed_access: access, Parent_fd: int32(p)}
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(ruleset), unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&attr)), 0, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_add_rule %s: %w", g.Path, errno)
	}
	return nil
}
```

`rights(1)` must equal `0x1FFF`; add `TestRightsByABI` checking 1 → `0x1FFF`, 2 → `0x3FFF`,
3 → `0x7FFF`, 5 → `0xFFFF`.

- [ ] **Step 4: Write the spec, the builder and the helper**

In `sandbox/sandbox.go` (package doc moves here from `grants.go`):

```go
// HelperArg is the argument that makes the ganglion binary the sandbox's
// helper: `ganglion __sandbox`, with the spec on file descriptor 3.
const HelperArg = "__sandbox"

// exitSetup is the helper's exit code when it could not set up the
// sandbox or start the command: the command never ran.
const exitSetup = 125

// exitReadable is the helper's exit code when the self-test could read
// the file it should not (spec.Try).
const exitReadable = 126

// specFD is the file descriptor the helper reads its spec from.
const specFD = 3

// spec is what the node hands the helper.
type spec struct {
	Argv   []string
	Env    []string // the whole environment of the command
	Dir    string   // its working folder
	Grants []Grant
	ABI    int
	Try    string // the self-test: a denied file the helper tries to read instead of exec'ing
}

// command is an exec.Cmd that runs self as the helper with sp on fd 3. The
// helper's own environment is sp.Env, so not even the helper sees the
// node's variables.
func command(ctx context.Context, self string, sp spec) (*exec.Cmd, error) {
	f, err := specFile(sp)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, self, HelperArg)
	cmd.Env, cmd.Dir, cmd.ExtraFiles = sp.Env, "/", []*os.File{f}
	return cmd, nil
}
```

`specFile` is Linux-only (`sandbox/landlock_linux.go` is a good home, or a `spec_linux.go`):

```go
// specFile is sp as JSON in an anonymous memory file, read from its start.
func specFile(sp spec) (*os.File, error) {
	fd, err := unix.MemfdCreate("ganglion-sandbox", unix.MFD_CLOEXEC)
	if err != nil {
		return nil, fmt.Errorf("sandbox: memfd: %w", err)
	}
	f := os.NewFile(uintptr(fd), "sandbox-spec")
	if err := json.NewEncoder(f).Encode(sp); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sandbox: spec: %w", err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("sandbox: spec: %w", err)
	}
	return f, nil
}
```

The caller closes the file after `cmd.Start` (exec dups it into the child): `command` returns it in
`cmd.ExtraFiles`, and `Sandbox.Command` (task 5) documents that `cmd.Wait` is not enough; in the tests
above `cmd.Run` holds it until the end, which is fine. In `sandbox/spec_other.go` (`//go:build !linux`), `specFile` returns
`errNotLinux`, declared there as `errors.New("sandbox: Linux only")`.

`sandbox/helper_linux.go`:

```go
//go:build linux

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

// Helper is the sandbox's helper: it reads the spec from fd 3, sets
// no_new_privs, confines itself with Landlock and execs the command, on
// one locked thread, so the flag and the domain are the ones exec carries.
// It returns only when that fails, with exitSetup: the command never ran.
func Helper() int {
	runtime.LockOSThread()
	if err := helper(); err != nil {
		fmt.Fprintln(os.Stderr, "ganglion sandbox:", err)
		return exitSetup
	}
	return 0 // unreachable: exec replaced the process, or the self-test exited
}

func helper() error {
	f := os.NewFile(specFD, "sandbox-spec")
	var sp spec
	err := json.NewDecoder(f).Decode(&sp)
	_ = f.Close()
	if err != nil {
		return fmt.Errorf("spec: %w", err)
	}
	if len(sp.Argv) == 0 && sp.Try == "" {
		return errors.New("spec: no command")
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if err := restrict(sp.ABI, sp.Grants); err != nil {
		return err
	}
	if sp.Try != "" {
		tryRead(sp.Try)
	}
	prog, err := exec.LookPath(sp.Argv[0]) // PATH is the spec's: the helper's environment is sp.Env
	if err != nil {
		return err
	}
	if err := os.Chdir(sp.Dir); err != nil {
		return err
	}
	return unix.Exec(prog, sp.Argv, sp.Env)
}

// tryRead is the self-test: it exits 0 when path is denied and
// exitReadable when it reads.
func tryRead(path string) {
	if _, err := os.ReadFile(path); errors.Is(err, os.ErrPermission) {
		os.Exit(0)
	}
	os.Exit(exitReadable)
}
```

Add a `sandbox/helper_other.go` (`//go:build !linux`) whose `Helper` prints "the sandbox is Linux
only" and returns `exitSetup`.

- [ ] **Step 5: Run the abuse tests**

Run: `go test -race ./sandbox/`
Expected: PASS on the owner's machine (Landlock present); every helper test reports `SKIP` where
there is none. `TestSudoGainsNothing` passes because `sudo` refuses under `no_new_privs`.

- [ ] **Step 6: Lint, then commit**

Run: `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run ./sandbox/...`
Expected: 0 issues. A `gosec` note on `unsafe` or `exec` gets a `//nolint` with its reason, as
elsewhere in the repository.

```bash
git add sandbox go.mod go.sum
git commit -m "sandbox: the helper sets no_new_privs and Landlock, then execs the command"
```

### Task 5: The sandbox: checks at start, the self-test, `Command`

**Files:**
- Create: `sandbox/kernel_linux.go`, `sandbox/kernel_other.go`, `sandbox/sandbox_test.go`
  (`//go:build linux`: off for its own reasons there, since every other OS is `NotLinux` first)
- Modify: `sandbox/sandbox.go`

**Interfaces:**
- Consumes: `command`, `spec`, `Grants`, `DenyList`, `EnvFiles`, `landlockABI` (tasks 3–4).
- Produces:
  - `type Reason int` with `Ready, NotLinux, Root, NoLandlock, Dumpable, SelfTestFailed`.
  - `type Kernel interface { Euid() int; LandlockABI() (int, error); Dumpable() (bool, error) }`;
    `func HostKernel() Kernel`.
  - `type Policy struct { Deny []string; Env []string; EnvRoots []string; EnvDepth int }`.
  - `func New(self string, p Policy, k Kernel, now time.Time) *Sandbox`.
  - `func (s *Sandbox) Ready() (bool, Reason)`.
  - `func (s *Sandbox) Command(ctx context.Context, argv []string, dir string) (*exec.Cmd, error)`:
    `ErrOff` when not ready.

- [ ] **Step 1: Write the failing tests**

```go
package sandbox

import (
	"errors"
	"os"
	"testing"
	"time"
)

type fakeKernel struct {
	euid     int
	abi      int
	abiErr   error
	dumpable bool
}

func (k fakeKernel) Euid() int                    { return k.euid }
func (k fakeKernel) LandlockABI() (int, error)    { return k.abi, k.abiErr }
func (k fakeKernel) Dumpable() (bool, error)      { return k.dumpable, nil }

func TestNewIsOffForEachMissingPiece(t *testing.T) {
	cases := map[string]struct {
		k    fakeKernel
		want Reason
	}{
		"root":        {fakeKernel{euid: 0, abi: 5}, Root},
		"no landlock": {fakeKernel{euid: 1000, abiErr: errors.New("ENOSYS")}, NoLandlock},
		"dumpable":    {fakeKernel{euid: 1000, abi: 5, dumpable: true}, Dumpable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			s := New("/nonexistent", Policy{}, c.k, time.Unix(0, 0))
			if ok, r := s.Ready(); ok || r != c.want {
				t.Fatalf("Ready() = %v, %v; want false, %v", ok, r, c.want)
			}
			if _, err := s.Command(t.Context(), []string{"true"}, "/"); !errors.Is(err, ErrOff) {
				t.Fatalf("Command on an off sandbox: %v, want ErrOff", err)
			}
		})
	}
}

func TestNewIsOffWhenTheSelfTestCannotRun(t *testing.T) {
	s := New("/nonexistent", Policy{}, fakeKernel{euid: 1000, abi: 5}, time.Unix(0, 0))
	if ok, r := s.Ready(); ok || r != SelfTestFailed {
		t.Fatalf("Ready() = %v, %v; want false, SelfTestFailed", ok, r)
	}
}
```

In `helper_test.go` (linux, it needs the real helper) add the ready case:

```go
func TestNewIsReadyWhereTheKernelHoldsIt(t *testing.T) {
	abi, err := landlockABI()
	if err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := New(self, Policy{Env: []string{"PATH=/usr/bin:/bin"}}, fakeKernel{euid: 1000, abi: abi}, time.Unix(0, 0))
	if ok, r := s.Ready(); !ok {
		t.Fatalf("not ready: %v", r)
	}
	dir, file := secretDir(t)
	s = New(self, Policy{Deny: []string{dir}, Env: []string{"PATH=/usr/bin:/bin"}}, fakeKernel{euid: 1000, abi: abi}, time.Unix(0, 0))
	cmd, err := s.Command(t.Context(), []string{"cat", file}, "/")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(out), "hunter2") {
		t.Fatalf("read a denied file through Command: %v, %q", err, out)
	}
}
```

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./sandbox/ -run 'TestNew'`
Expected: FAIL, `undefined: New`.

- [ ] **Step 3: Write the kernel checks**

`sandbox/kernel_linux.go`:

```go
//go:build linux

package sandbox

import (
	"os"

	"golang.org/x/sys/unix"
)

type hostKernel struct{}

// HostKernel is the running kernel and process.
func HostKernel() Kernel { return hostKernel{} }

func (hostKernel) Euid() int                 { return os.Geteuid() }
func (hostKernel) LandlockABI() (int, error) { return landlockABI() }

// Dumpable reports whether this process's /proc files are open to its own
// user; the node clears it at start (cmd/harden_linux.go).
func (hostKernel) Dumpable() (bool, error) {
	v, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	return v != 0, err
}
```

`sandbox/kernel_other.go` (`//go:build !linux`): `HostKernel` returns a `Kernel` whose
`LandlockABI` returns `errNotLinux`; `New` checks `runtime.GOOS != "linux"` first and is off with
`NotLinux`.

- [ ] **Step 4: Write `New`, `Ready` and `Command`**

```go
// Reason is why the sandbox is off; Ready when it is on.
type Reason int

// The reasons, in the order New checks them.
const (
	Ready Reason = iota
	NotLinux
	Root
	NoLandlock
	Dumpable
	SelfTestFailed
)

// ErrOff is Command on a sandbox that is off: the command does not run.
var ErrOff = errors.New("sandbox: off")

// Kernel is what the sandbox needs to know of the process and its kernel.
type Kernel interface {
	Euid() int
	LandlockABI() (int, error)
	Dumpable() (bool, error)
}

// Policy is what every command is denied and the environment it gets.
type Policy struct {
	Deny     []string // DenyList
	Env      []string // the whole environment, KEY=VALUE
	EnvRoots []string // where .env files are looked for, besides the command's folder
	EnvDepth int
}

// Sandbox starts commands confined by its policy, or none when it is off.
type Sandbox struct {
	self   string
	policy Policy
	abi    int
	reason Reason
	since  time.Time
}

// New checks, once, that the sandbox holds here: Linux, not root, Landlock
// present, the node not dumpable, and a self-test that runs the helper and
// fails to read a denied file. The first that fails turns it off; that
// is final until a restart (R-CMD-3, fail closed).
func New(self string, p Policy, k Kernel, now time.Time) *Sandbox {
	s := &Sandbox{self: self, policy: p, since: now}
	s.reason = s.check(k)
	return s
}

func (s *Sandbox) check(k Kernel) Reason {
	if runtime.GOOS != "linux" {
		return NotLinux
	}
	if k.Euid() == 0 {
		return Root
	}
	abi, err := k.LandlockABI()
	if err != nil {
		return NoLandlock
	}
	s.abi = abi
	if d, err := k.Dumpable(); err != nil || d {
		return Dumpable
	}
	if !s.selfTest() {
		return SelfTestFailed
	}
	return Ready
}

// selfTest runs the helper against a file it must not read.
func (s *Sandbox) selfTest() bool {
	dir, err := os.MkdirTemp("", "ganglion-sandbox-")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "secret")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		return false
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	grants, err := Grants([]string{real}, os.ReadDir)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := command(ctx, s.self, spec{Env: s.policy.Env, Dir: "/", Grants: grants, ABI: s.abi,
		Try: filepath.Join(real, "secret")})
	if err != nil {
		return false
	}
	defer closeSpec(cmd)
	return cmd.Run() == nil
}

// Ready reports whether commands may run, and why not.
func (s *Sandbox) Ready() (bool, Reason) { return s.reason == Ready, s.reason }

// Command is argv run in dir inside the sandbox: the caller sets stdio, a
// process group and the time limit, starts it, and calls Release after
// Start. The .env files under dir and the policy's roots are
// denied with the rest.
func (s *Sandbox) Command(ctx context.Context, argv []string, dir string) (*exec.Cmd, error) {
	if s.reason != Ready {
		return nil, ErrOff
	}
	deny := append(slices.Clone(s.policy.Deny), EnvFiles(append([]string{dir}, s.policy.EnvRoots...), s.policy.EnvDepth, os.ReadDir)...)
	grants, err := Grants(deny, os.ReadDir)
	if err != nil {
		return nil, err
	}
	return command(ctx, s.self, spec{Argv: argv, Env: s.policy.Env, Dir: dir, Grants: grants, ABI: s.abi})
}

// Release closes the spec file a started command no longer needs.
func Release(cmd *exec.Cmd) { closeSpec(cmd) }

func closeSpec(cmd *exec.Cmd) {
	for _, f := range cmd.ExtraFiles {
		_ = f.Close()
	}
}
```

`EnvFiles` returns paths through `filepath.Join`; resolve them with `filepath.EvalSymlinks` the same
way `DenyList` does before they join `deny` (a `.env` reached through a symlinked folder).

- [ ] **Step 5: Run the tests**

Run: `go test -race ./sandbox/`
Expected: PASS; the linux test `TestNewIsReadyWhereTheKernelHoldsIt` runs on the owner's machine.

- [ ] **Step 6: Lint, then commit**

```bash
git add sandbox
git commit -m "sandbox: checks at start and a self-test turn it off with a reason; Command"
```

### Task 6: The node starts the helper, `/status` shows the sandbox

**Files:**
- Create: `cmd/sandbox.go`, `config/sandbox.go`, `sandbox/status.go`, `sandbox/status_test.go`
- Modify: `cmd/root.go`, `cmd/flags/flags.go`, `cmd/run.go`, `config/bootstrap.go`,
  `config/status.go`, `config/config.go` (register the provider), `tools/chatlab/main.go`,
  `texts/texts.go`, `texts/lang/{en,ru,uk}.yaml`, `tools/chatlab/scenarios/mech-status.yaml`,
  `tools/chatlab/no-scenario.txt`

**Interfaces:**
- Consumes: `New`, `Policy`, `DenyList`, `NodeFiles`, `HostKernel`, `HelperArg`, `Helper`.
- Produces:
  - `func (s *Sandbox) Name() string` ("sandbox") and `Status(ctx, status.Scope) ([]status.Check, error)`.
  - `Bootstrap.SandboxDeny []string` from `--sandbox-deny` / `GANGLION_SANDBOX_DENY`
    (colon-separated paths).
  - a `*sandbox.Sandbox` in the injector, for 3.22 (`host.exec`) and 3.26 (skill scripts).

- [ ] **Step 1: Texts.** Keys in `texts/texts.go` beside the other `status.*` keys:

```go
	StatusSandboxReady          Key = "status.sandbox_ready"
	StatusSandboxOff            Key = "status.sandbox_off"
	StatusReasonSandboxNotLinux Key = "status.reason_sandbox_not_linux"
	StatusReasonSandboxRoot     Key = "status.reason_sandbox_root"
	StatusReasonSandboxLandlock Key = "status.reason_sandbox_landlock"
	StatusReasonSandboxDumpable Key = "status.reason_sandbox_dumpable"
	StatusReasonSandboxSelfTest Key = "status.reason_sandbox_self_test"
```

`en.yaml`:

```yaml
status.sandbox_ready: "Command sandbox: ready, Landlock {abi}"
status.sandbox_off: "Command sandbox: off"
status.reason_sandbox_not_linux: "this is not Linux"
status.reason_sandbox_root: "the node runs as root"
status.reason_sandbox_landlock: "the kernel has no Landlock"
status.reason_sandbox_dumpable: "the node's memory is open to its user"
status.reason_sandbox_self_test: "its self-test failed"
```

`ru.yaml`:

```yaml
status.sandbox_ready: "Песочница команд: готова, Landlock {abi}"
status.sandbox_off: "Песочница команд: выключена"
status.reason_sandbox_not_linux: "это не Linux"
status.reason_sandbox_root: "нода работает от root"
status.reason_sandbox_landlock: "в ядре нет Landlock"
status.reason_sandbox_dumpable: "память ноды открыта её пользователю"
status.reason_sandbox_self_test: "её самопроверка не прошла"
```

`uk.yaml`:

```yaml
status.sandbox_ready: "Пісочниця команд: готова, Landlock {abi}"
status.sandbox_off: "Пісочниця команд: вимкнена"
status.reason_sandbox_not_linux: "це не Linux"
status.reason_sandbox_root: "нода працює від root"
status.reason_sandbox_landlock: "у ядрі немає Landlock"
status.reason_sandbox_dumpable: "пам'ять ноди відкрита її користувачу"
status.reason_sandbox_self_test: "її самоперевірка не пройшла"
```

- [ ] **Step 2: Write the failing status test**

```go
package sandbox

import (
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

func TestStatusSaysWhyItIsOff(t *testing.T) {
	s := New("/nonexistent", Policy{}, fakeKernel{euid: 0, abi: 5}, time.Unix(0, 0))
	got, err := s.Status(t.Context(), status.Scope{})
	if err != nil {
		t.Fatal(err)
	}
	want := status.Check{Section: status.SectionProblems, State: status.Off, Line: texts.StatusSandboxOff,
		Reason: texts.StatusReasonSandboxRoot, Since: time.Unix(0, 0)}
	if len(got) != 1 || got[0].Line != want.Line || got[0].State != want.State || got[0].Reason != want.Reason {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestStatusReadyNamesTheABI(t *testing.T) {
	s := &Sandbox{reason: Ready, abi: 6}
	got, _ := s.Status(t.Context(), status.Scope{})
	if len(got) != 1 || got[0].State != status.OK || got[0].Vars["abi"] != "6" {
		t.Fatalf("got %+v", got)
	}
}
```

- [ ] **Step 3: Run it to see it fail**, then write `sandbox/status.go`:

```go
package sandbox

import (
	"context"
	"strconv"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// offReasons are the reasons' texts in /status.
var offReasons = map[Reason]texts.Key{
	NotLinux: texts.StatusReasonSandboxNotLinux, Root: texts.StatusReasonSandboxRoot,
	NoLandlock: texts.StatusReasonSandboxLandlock, Dumpable: texts.StatusReasonSandboxDumpable,
	SelfTestFailed: texts.StatusReasonSandboxSelfTest,
}

// Name implements status.Source.
func (*Sandbox) Name() string { return "sandbox" }

// Status implements status.Source: ready with Landlock's version, or off
// with its reason. Off is not a warning: a node may run without commands.
func (s *Sandbox) Status(context.Context, status.Scope) ([]status.Check, error) {
	if s.reason == Ready {
		return []status.Check{{Section: status.SectionProblems, State: status.OK, Line: texts.StatusSandboxReady,
			Vars: texts.Vars{"abi": strconv.Itoa(s.abi)}}}, nil
	}
	return []status.Check{{Section: status.SectionProblems, State: status.Off, Line: texts.StatusSandboxOff,
		Reason: offReasons[s.reason], Since: s.since}}, nil
}
```

Run: `go test ./sandbox/ ./texts/`
Expected: PASS (`TestShippedFilesMatchEnglish` included).

- [ ] **Step 4: The hidden command.** `cmd/sandbox.go`:

```go
package cmd

import (
	"context"
	"os"

	"github.com/igorrius/ganglion/sandbox"
	"github.com/urfave/cli/v3"
)

// sandboxCommand is the sandbox's helper (ADR-0039): the node starts its
// own binary as `ganglion __sandbox` for each command. Hidden: nobody types it.
func sandboxCommand() *cli.Command {
	return &cli.Command{
		Name:            sandbox.HelperArg,
		Hidden:          true,
		SkipFlagParsing: true,
		Action: func(context.Context, *cli.Command) error {
			os.Exit(sandbox.Helper()) // returns only when the command could not start
			return nil
		},
	}
}
```

Add `sandboxCommand()` to `Commands` in `cmd/root.go`. A test in `cmd/cmd_test.go`: the root's
help does not list `__sandbox` (run `ganglion --help`, check the output). If a command-list test
fails, it now has a hidden entry: update it.

In `tools/chatlab/main.go`, before the switch on the first argument, the chatlab binary is the node
there, so it is the helper too:

```go
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperArg {
		os.Exit(sandbox.Helper())
	}
```

- [ ] **Step 5: The flag.** In `cmd/flags/flags.go`, beside `--workspace`:

```go
		&cli.StringFlag{Name: "sandbox-deny", Usage: "more paths, colon-separated, that commands may neither read nor write",
			Category: catAgent, Local: true, Sources: env("GANGLION_SANDBOX_DENY"), Destination: &r.SandboxDeny},
```

with `SandboxDeny string` in `flags.Run`; in `cmd/run.go`'s `bootstrap`,
`b.SandboxDeny = filepath.SplitList(v.SandboxDeny)`; in `config.Bootstrap`:

```go
	// SandboxDeny is GANGLION_SANDBOX_DENY: paths the owner adds to what
	// commands may neither read nor write (R-CMD-3).
	SandboxDeny []string
```

A `cmd_test.go` case: `GANGLION_SANDBOX_DENY=/a:/b` gives `[]string{"/a", "/b"}`.

- [ ] **Step 6: The provider.** `config/sandbox.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/sandbox"
	"github.com/samber/do/v2"
)

// envDepth is how deep the sandbox looks for .env files (the spike's note).
const envDepth = 3

// Sandbox provides the command sandbox, checked once at start (R-CMD-3,
// ADR-0039). Off is not an error: the node runs, without commands.
func Sandbox() do.Provider[*sandbox.Sandbox] {
	return func(i do.Injector) (*sandbox.Sandbox, error) {
		b, start := do.MustInvoke[Bootstrap](i), time.Time(do.MustInvoke[ProcessStart](i))
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		home, _ := os.UserHomeDir() // none: no home secrets to deny, and HOME=/
		node := sandbox.NodeFiles{DB: b.DB, LogDir: b.LogDir, ModulesDir: b.ModulesDir,
			Workspace: b.Agent.Workspace, Languages: b.Languages}
		p := sandbox.Policy{
			Deny:     sandbox.DenyList(home, node, os.Environ(), b.SandboxDeny),
			Env:      commandEnv(home, b),
			EnvRoots: []string{home},
			EnvDepth: envDepth,
		}
		return sandbox.New(self, p, sandbox.HostKernel(), start), nil
	}
}

// commandEnv is the whole environment a command gets (design section 5).
func commandEnv(home string, b Bootstrap) []string {
	if home == "" {
		home = "/"
	}
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + filepath.Clean(home),
		"LANG=C.UTF-8", "TZ=" + b.Agent.TZ.String(), "TERM=dumb"}
}
```

`b.Agent.TZ` may be nil when the agent is off; fall back to `"UTC"`. Register `Sandbox()` in
`config/config.go` beside the other providers, and add `do.MustInvoke[*sandbox.Sandbox](i)` to the
list in `config/status.go`.

Log once at start when it is off, through the logger the neighbouring providers take from the
injector: `Info("sandbox: commands are off", "reason", r)`
(a `Reason.String()` from a fixed table: `not_linux`, `root`, `no_landlock`, `dumpable`,
`self_test`; add it with a test).

- [ ] **Step 7: Chatlab claims.** Add `sandbox/status.go` and `config/sandbox.go` to `mech-status`'s
`covers`; add `sandbox/grants.go`, `sandbox/deny.go`, `sandbox/sandbox.go`, `sandbox/*_linux.go`,
`sandbox/*_other.go` and `cmd/sandbox.go` to `tools/chatlab/no-scenario.txt` (unit tests hold
them).

Run: `go test ./internal/chatlab/ -run TestEveryProductionFileIsClaimed`
Expected: PASS.

- [ ] **Step 8: Everything, then the scenario**

Run: `go test -race ./sandbox/ ./cmd/... ./config/ ./texts/ && go test ./... && go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0 run && make lab LAB=mech-status`
Expected: PASS; 0 issues; `mech-status` 3 of 3, its transcript has the line "Command sandbox:
ready, Landlock N" (chatlab's node is not root and is not dumpable only if chatlab calls
`hardenProcess`; if it does not, the line reads "off — the node's memory is open to its user",
which is the truth there: note it in the commit and leave it).

- [ ] **Step 9: Commit**

```bash
git add cmd config sandbox texts tools/chatlab
git commit -m "sandbox: the node starts its own binary as the helper; /status shows the sandbox"
```

### Task 7: Accept the decision; docs; measure; review

**Files:**
- Modify: `docs/adr/0039-command-sandbox.md`, `docs/adr/README.md`, `docs/spec.md`,
  `docs/architecture/05-code-map.md`, `docs/README.md`, `docs/plans/p1-all-in-one.md`, this plan.

- [ ] **Step 1: ADR-0039 → Accepted** if task 2 held on every host the node runs on, with a
  "Spike" paragraph linking the research note; else it stays Proposed and the note says what is
  open. The row in `docs/adr/README.md` follows.
- [ ] **Step 2: Spec.** Section 11's open decision "Sandbox for scripts and MCP modules": closed for
  commands and scripts by ADR-0039 (MCP modules in P3). R-CMD-3: add "the owner adds paths with
  `GANGLION_SANDBOX_DENY`" and the `.env` depth from the note.
- [ ] **Step 3: Code map.** `sandbox` (leaf; imports `status`, `texts`) with one line per file; the
  dependency diagram gets `config --> sandbox` and `sandbox --> status`; `tools/sandboxprobe`.
- [ ] **Step 4: Measure.** `make memory`: the binary's size and the agent's RSS against `master`'s
  (ADR-0056 compares, it does not fail); write both in "Where we are".
- [ ] **Step 5: Whole-branch review** with superpowers:requesting-code-review, the Review Focus above
  as its brief; fix each finding or record it with its reason.
- [ ] **Step 6: "Where we are"** in `docs/README.md`: 3.20 closed, what the spike found per host,
  the minors left; `p1-all-in-one.md`'s 3.20 row "closed 2026-11-…"; tick this plan.
- [ ] **Step 7: The owner's look**: `/status` on the test bot shows the sandbox line; then commit
  `docs: the command sandbox done`, and ask the owner about the merge.
