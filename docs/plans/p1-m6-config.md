# P1 · M6 tasks 6.1–6.3: the node's file through the agent — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task.
> Steps use checkbox (`- [x]`) syntax.

**Goal:** the agent changes routine fields of `ganglion.yaml` with the owner's press, every
change to the file is recorded in the workspace repository, and a change can be rolled back
([#236](https://github.com/igorrius/ganglion/issues/236)).

**Architecture:**

- **`settings`:** every writer refuses to leave a file the node would not start with;
  `SetPath` sets values by dotted path in the YAML tree; `Values` reads them; `Allowed` is the
  allowlist (R-CFG-14).
- **`gitrepo`:** `Log`, `Diff` and `FileAt`, so the history can be listed and a change read.
- **New package `confighist`:** the recorder. It wraps every writer of the file: write, copy
  to `config/ganglion.yaml` in the workspace, commit with a reason and a source. `Sync`
  commits a host edit at start. `NoHistory` stands in on a node without git.
- **`tool/builtin`:** `config.propose`, `config.history`, `config.rollback` and the
  `config.restart` button.
- **`config`:** the recorder's provider; the existing writers are wrapped by it; the tools and
  the button are registered.

**Tech stack:** Go 1.27, `go.yaml.in/yaml/v3` (already used by `settings`), the git binary
through `gitrepo`. No new dependency.

**Spec:** 7.14 (R-CFG-6, R-CFG-13 – R-CFG-16), 7.8 (R-POL-6, R-POL-9);
[ADR-0093](../adr/0093-config-changes-through-the-agent-with-history-in-the-workspace.md),
[ADR-0059](../adr/0059-node-config-file.md); the [design note](p1-m6-config-design.md). Read
them with this plan.

**Status (2026-11-16):** done. Merged in #237, released in `v0.5.0`, installed on the owner's
node and checked live by the owner the same day. The live check found the node silent after a
restart: #238. Where the code differs from the text below:
`confighist.New` takes a logger as its third argument; the provider is `config.FileHistory`
(the linter refused `ConfigHistory`), and a node with no agent keeps no history, so an echo
node does not create a workspace; the question's texts are `ask.config_propose`,
`ask.config_tainted`, `ask.config_kept` and `ask.config_default`; group changes are recorded by
chat id, never by title, since a title is written by the group's members.

## Global Constraints

- All documentation, comments and doc comments are in English; the
  [conventions](../conventions.md) hold (small functions, sentinel errors wrapped as
  `pkg: action: %w`, no package-level mutable state beyond what exists, no nil dependencies,
  time injected, fakes in `<pkg>test` packages, no sleeping in tests).
- Domain packages never import `samber/do`; only `config` builds concrete types.
- The allowlist is exactly R-CFG-14: `node.tz`, `node.memory_limit`, `log.level`,
  `telegram.reply`, `telegram.rich`, `telegram.reactions.after`, `telegram.reactions.dwell`,
  `agent.languages`, `agent.session_idle`, `agent.call_timeout`, `agent.turn_timeout`,
  `agent.limits.daily_budget_usd`, `agent.limits.daily_turns`, `agent.limits.daily_tokens`,
  `agent.search.mode`, `memory.prompt_budget`, `memory.review.enabled`, `memory.review.quiet`,
  `update.check`, `models.purposes`.
- The copy's path in the workspace is `config/ganglion.yaml`. Sources are exactly `propose`,
  `rollback`, `module`, `destination`, `group`, `host`.
- The tools are admin only, refused under a scheduled grant (as `routeTool.allowed` does), and
  `config.propose` / `config.rollback` are `tool.PolicyConfirm`.
- No value of a field outside the allowlist is ever shown by these tools in full: a secret
  reference is a field the tools never read.
- Nothing real about a running node goes into the repository: examples use `example.com`,
  `telegram:user:123456`.
- Run `make check` before each commit; chatlab scenarios with `make lab LAB=<name>` (the owner
  decides a full run).

---

### Task 1: `settings` — a writer never leaves a broken file; `Apply`, `SetPath`, `Values`, `Allowed`

**Files:**
- Modify: `settings/write.go` (`editDoc`: parse the result before `replaceFile`)
- Create: `settings/write_path.go`, `settings/allow.go`
- Test: `settings/write_path_test.go`, `settings/allow_test.go`, `settings/write_test.go`

**Interfaces:**
- Produces:
  - `type Change struct{ Path string; Value any }` — `Value` is what JSON decoding gives (string,
    float64, bool, `map[string]any`, `[]any`); `nil` removes the key (back to the default).
  - `func Apply(src []byte, cs []Change) ([]byte, error)` — the same edit on bytes in memory,
    returning the new file; refuses with a path error when the result fails `Parse`.
  - `func SetPath(path string, cs []Change) error` — `Apply` through `editDoc`: edits the YAML
    tree, keeps comments and order, refuses with a path error when the result fails `Parse`;
    the file is untouched then.
  - `func Values(src []byte, paths []string) (map[string]any, error)` — the decoded YAML value
    at each path; a missing key is absent from the map (it means the default).
  - `func Allowed(path string) bool` and `var AllowedPaths = []string{...}` (R-CFG-14, sorted).
  - `var ErrNotAllowed = errors.New("settings: the field is not changed from the chat")`.
  - `func Where(path string) Place` with `type Place string` and constants `PlaceHost`,
    `PlaceOwnTool`, `PlaceChat` — where a field is changed (R-CFG-14's refusal reason).

- [x] **Step 1: Write the failing tests.**

  The check added to `editDoc` is covered through `SetPath` below; the existing writers'
  tests in `settings/write*_test.go` must still pass with it (Step 4).

  In `settings/write_path_test.go`:

  ```go
  const pathSrc = `# the owner's comment
  agent:
    limits:
      daily_turns: 200 # keep low
  telegram:
    rich: true
  `

  func TestApplyReturnsTheEditedFile(t *testing.T) {
  	out, err := settings.Apply([]byte(pathSrc), []settings.Change{{Path: "telegram.rich", Value: false}})
  	if err != nil || !strings.Contains(string(out), "rich: false") || !strings.Contains(string(out), "# keep low") {
  		t.Fatalf("Apply = %q, %v", out, err)
  	}
  }

  func TestSetPathKeepsCommentsAndSetsNested(t *testing.T) {
  	path := write(t, pathSrc)
  	err := settings.SetPath(path, []settings.Change{
  		{Path: "agent.limits.daily_turns", Value: float64(300)},
  		{Path: "memory.review.quiet", Value: "2h"},
  	})
  	if err != nil {
  		t.Fatal(err)
  	}
  	got := read(t, path)
  	for _, want := range []string{"# the owner's comment", "daily_turns: 300 # keep low", "quiet: 2h", "rich: true"} {
  		if !strings.Contains(got, want) {
  			t.Errorf("missing %q in\n%s", want, got)
  		}
  	}
  }

  func TestSetPathRefusesABadValueAndKeepsTheFile(t *testing.T) {
  	path := write(t, pathSrc)
  	err := settings.SetPath(path, []settings.Change{{Path: "agent.limits.daily_turns", Value: "many"}})
  	if err == nil || !strings.Contains(err.Error(), "agent.limits.daily_turns") {
  		t.Fatalf("err = %v, want one naming the path", err)
  	}
  	if read(t, path) != pathSrc {
  		t.Fatal("the file changed")
  	}
  }

  func TestSetPathNilRemovesTheKey(t *testing.T) {
  	path := write(t, pathSrc)
  	if err := settings.SetPath(path, []settings.Change{{Path: "telegram.rich", Value: nil}}); err != nil {
  		t.Fatal(err)
  	}
  	if strings.Contains(read(t, path), "rich:") {
  		t.Fatal("rich is still there")
  	}
  }

  func TestValues(t *testing.T) {
  	got, err := settings.Values([]byte(pathSrc), []string{"agent.limits.daily_turns", "node.tz"})
  	if err != nil {
  		t.Fatal(err)
  	}
  	if got["agent.limits.daily_turns"] != 200 {
  		t.Fatalf("daily_turns = %#v", got["agent.limits.daily_turns"])
  	}
  	if _, ok := got["node.tz"]; ok {
  		t.Fatal("a missing key has a value")
  	}
  }
  ```

  `write` and `read` are small helpers in the test file (`os.WriteFile` / `os.ReadFile` in
  `t.TempDir()`); reuse existing ones in `settings_test` if the package has them.

  In `settings/allow_test.go`:

  ```go
  func TestAllowedIsTheSpecList(t *testing.T) {
  	want := []string{"agent.call_timeout", "agent.languages", "agent.limits.daily_budget_usd",
  		"agent.limits.daily_tokens", "agent.limits.daily_turns", "agent.search.mode",
  		"agent.session_idle", "agent.turn_timeout", "log.level", "memory.prompt_budget",
  		"memory.review.enabled", "memory.review.quiet", "models.purposes", "node.memory_limit",
  		"node.tz", "telegram.reactions.after", "telegram.reactions.dwell", "telegram.reply",
  		"telegram.rich", "update.check"}
  	if !slices.Equal(settings.AllowedPaths, want) {
  		t.Fatalf("AllowedPaths = %v", settings.AllowedPaths)
  	}
  }

  func TestEveryAllowedPathIsInTheSchema(t *testing.T) {
  	schema, err := schemas.Get("node.schema.json")
  	if err != nil {
  		t.Fatal(err)
  	}
  	for _, p := range settings.AllowedPaths {
  		if _, err := schemas.At(schema, schemaPointer(p)); err != nil {
  			t.Errorf("%s: %v", p, err)
  		}
  	}
  }

  func TestWhere(t *testing.T) {
  	for path, want := range map[string]settings.Place{
  		"agent.limits.daily_turns": settings.PlaceChat,
  		"telegram.token":           settings.PlaceHost,
  		"telegram.admin":           settings.PlaceHost,
  		"telegram.api_url":         settings.PlaceHost,
  		"models.providers":         settings.PlaceHost,
  		"sandbox.deny":             settings.PlaceHost,
  		"http.public_url":          settings.PlaceOwnTool,
  		"telegram.updates":         settings.PlaceOwnTool,
  		"telegram.groups":          settings.PlaceOwnTool,
  		"destinations":             settings.PlaceOwnTool,
  		"modules":                  settings.PlaceOwnTool,
  		"agent.limits":             settings.PlaceHost, // a section, not a field
  	} {
  		if got := settings.Where(path); got != want {
  			t.Errorf("Where(%q) = %q, want %q", path, got, want)
  		}
  	}
  }
  ```

  `schemaPointer` turns `agent.limits.daily_turns` into the JSON pointer the schema uses for it
  (follow `$ref`s the way `schema_test.go` reads `/$defs/...`; if `schemas.At` does not follow
  `$ref`, walk `properties` and resolve `$ref` by hand in the test).

- [x] **Step 2: Run them, expect failures.** `go test ./settings/` — undefined `SetPath`,
  `Apply`, `Values`, `Allowed`, `Where`.

- [x] **Step 3: Implement.**

  `editDoc` in `settings/write.go`, after `enc.Close()` and before `replaceFile`:

  ```go
  	// A writer never leaves a file the node would not start with (R-CFG-15).
  	if _, err := Parse(out.Bytes()); err != nil {
  		return err
  	}
  ```

  `settings/allow.go`:

  ```go
  // AllowedPaths are the fields config.propose changes (R-CFG-14,
  // ADR-0093), sorted. A field is added here on purpose, never by a
  // schema change alone.
  var AllowedPaths = []string{ /* the list of TestAllowedIsTheSpecList */ }

  // Place is where a field of the node's file is changed.
  type Place string

  const (
  	PlaceChat    Place = "chat"     // config.propose
  	PlaceOwnTool Place = "own-tool" // modules, destinations, groups, the update mode
  	PlaceHost    Place = "host"     // everything else
  )

  // ownTool are the paths, and the prefixes of paths, that have a tool of their own.
  var ownTool = []string{"modules", "destinations", "telegram.groups", "telegram.updates", "http"}

  func Allowed(path string) bool { _, ok := slices.BinarySearch(AllowedPaths, path); return ok }

  func Where(path string) Place {
  	switch {
  	case Allowed(path):
  		return PlaceChat
  	case slices.ContainsFunc(ownTool, func(p string) bool { return path == p || strings.HasPrefix(path, p+".") || strings.HasPrefix(path, p+"[") }):
  		return PlaceOwnTool
  	default:
  		return PlaceHost
  	}
  }
  ```

  `settings/write_path.go`: one unexported `setPaths(doc *yaml.Node, cs []Change) error` is
  the edit; `SetPath` runs it through `editDoc`, and `Apply` unmarshals `src`, runs it,
  encodes with indent 2 and `Parse`s the result. In `setPaths`, for each
  change: split the path on `.`; walk from `rootOf(doc)` creating mappings with `child(m, key,
  yaml.MappingNode, tagMap)` for every segment but the last; for the last, `nil` removes the
  key pair (and then `dropEmpty` the parents that became empty), otherwise encode the value with
  `var n yaml.Node; n.Encode(value)` and replace the value node, keeping its `LineComment` and
  `HeadComment`. A float64 with no fraction is encoded as an int (JSON gives 300 as 300.0).
  Validation comes from `editDoc`.

  `Values` parses `src` into a `yaml.Node`, walks each path and decodes the found node with
  `node.Decode(&v)` into `any`.

- [x] **Step 4: Run the tests.** `go test ./settings/` — PASS. Also `go test ./...` — the
  existing writers' tests still pass with the new check.

- [x] **Step 5: Commit.** `settings: SetPath, Values and the allowlist; a writer never leaves a broken file`
  with `Refs #236`.

### Task 2: `gitrepo` — `Log`, `Diff`, `FileAt`

**Files:**
- Modify: `gitrepo/gitrepo.go`
- Test: `gitrepo/gitrepo_test.go`

**Interfaces:**
- Produces:
  - `type Entry struct{ ID string; When time.Time; Message string }` — `Message` is the whole
    message (subject, blank line, trailers).
  - `func (r *Repo) Log(ctx context.Context, rel string, n int) ([]Entry, error)` — newest
    first, at most `n`, commits that touched `rel`; an empty repository gives none.
  - `func (r *Repo) Diff(ctx context.Context, commit, rel string) (string, error)` — the
    unified diff of `rel` in `commit` against its parent (the whole file as added for a root
    commit).
  - `func (r *Repo) FileAt(ctx context.Context, commit, rel string, parent bool) ([]byte, bool, error)`
    — the file at `commit` (or its parent when `parent`); `false` when it did not exist there.

- [x] **Step 1: Write the failing test.**

  ```go
  func TestLogDiffFileAt(t *testing.T) {
  	ctx := context.Background()
  	r := open(t) // the existing helper that opens a repo in t.TempDir()
  	put := func(text, msg string) string {
  		p, _ := r.Path("config/ganglion.yaml")
  		os.MkdirAll(filepath.Dir(p), 0o750)
  		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
  			t.Fatal(err)
  		}
  		id, err := r.Commit(ctx, []string{"config/ganglion.yaml"}, msg)
  		if err != nil {
  			t.Fatal(err)
  		}
  		return id
  	}
  	first := put("a: 1\n", "first\n\nSource: host")
  	second := put("a: 2\n", "second\n\nSource: propose")

  	log, err := r.Log(ctx, "config/ganglion.yaml", 10)
  	if err != nil || len(log) != 2 || log[0].ID != second || !strings.HasPrefix(log[0].Message, "second") {
  		t.Fatalf("Log = %+v, %v", log, err)
  	}
  	d, err := r.Diff(ctx, second, "config/ganglion.yaml")
  	if err != nil || !strings.Contains(d, "-a: 1") || !strings.Contains(d, "+a: 2") {
  		t.Fatalf("Diff = %q, %v", d, err)
  	}
  	before, ok, err := r.FileAt(ctx, second, "config/ganglion.yaml", true)
  	if err != nil || !ok || string(before) != "a: 1\n" {
  		t.Fatalf("FileAt parent = %q %v %v", before, ok, err)
  	}
  	_, ok, err = r.FileAt(ctx, first, "config/ganglion.yaml", true)
  	if err != nil || ok {
  		t.Fatalf("a root commit's parent has the file: %v %v", ok, err)
  	}
  	if _, err := r.Diff(ctx, "not-a-commit", "config/ganglion.yaml"); err == nil {
  		t.Fatal("a bad commit id was passed to git")
  	}
  }
  ```

- [x] **Step 2: Run it, expect a failure.** `go test ./gitrepo/ -run TestLogDiffFileAt`.

- [x] **Step 3: Implement** with the package's existing way of running git (the unexported
  runner `Last` uses), under `r.mu`, with `checkPath(rel)` and `commitPattern` on every id.
  - `Log`: `git log -n <n> --format=%H%x00%ct%x00%B%x1e -- <rel>`; split on `\x1e`, then
    `\x00`; an empty repository (no HEAD) returns `nil, nil`.
  - `Diff`: `git show --format= --no-color <commit> -- <rel>` (works for a root commit too).
  - `FileAt`: `git cat-file -e <rev>:<rel>` to test existence, then `git show <rev>:<rel>`;
    `rev` is `commit` or `commit + "^"`; a root commit's `^` does not exist, which is `false`.

- [x] **Step 4: Run the package tests.** `go test -race ./gitrepo/` — PASS.

- [x] **Step 5: Commit.** `gitrepo: Log, Diff and FileAt for the config history` with `Refs #236`.

### Task 3: `confighist` — the recorder

**Files:**
- Create: `confighist/confighist.go`, `confighist/files.go`, `confighist/telemetry.go`
- Test: `confighist/confighist_test.go`, `confighist/files_test.go`

**Interfaces:**
- Consumes: `settings.SetPath`, `settings.ModulesFile`, `settings.DestinationsFile`,
  `settings.TelegramFile` (Task 1); `gitrepo.Entry`, `Log`, `Diff`, `FileAt` (Task 2).
- Produces:

  ```go
  // Source is who changed the file (R-CFG-13).
  type Source string
  const (
  	SourcePropose     Source = "propose"
  	SourceRollback    Source = "rollback"
  	SourceModule      Source = "module"
  	SourceDestination Source = "destination"
  	SourceGroup       Source = "group"
  	SourceHost        Source = "host"
  )

  // Repo is the workspace repository (gitrepo.Repo).
  type Repo interface {
  	Path(rel string) (string, error)
  	Commit(ctx context.Context, paths []string, msg string) (string, error)
  	Log(ctx context.Context, rel string, n int) ([]gitrepo.Entry, error)
  	Diff(ctx context.Context, commit, rel string) (string, error)
  	FileAt(ctx context.Context, commit, rel string, parent bool) ([]byte, bool, error)
  }

  // Change is one recorded change of the file.
  type Change struct {
  	ID     string
  	When   time.Time
  	Source Source
  	Reason string
  }

  // History is the record of the node's file.
  type History interface {
  	// Record runs write under the history's lock, then copies the file
  	// and commits it with the reason and the source.
  	Record(ctx context.Context, src Source, reason string, write func() error) error
  	// Sync commits a live file that differs from the last copy (source host).
  	Sync(ctx context.Context) error
  	Changes(ctx context.Context, n int) ([]Change, error)
  	Diff(ctx context.Context, id string) (string, error)
  	// Versions are the file before and after the change id.
  	Versions(ctx context.Context, id string) (before, after []byte, err error)
  }

  const CopyPath = "config/ganglion.yaml"
  var ErrNoHistory = errors.New("confighist: this node keeps no history of its file (no git)")
  var ErrNoChange = errors.New("confighist: no such change")

  func New(file string, repo Repo) *Recorder          // implements History
  type NoHistory struct{}                               // Record only runs write; the rest return ErrNoHistory, Sync nil

  // Wrapped writers, the same methods as the settings types they wrap.
  type ModulesFile struct{ File settings.ModulesFile; History History }
  type DestinationsFile struct{ File settings.DestinationsFile; History History }
  type TelegramFile struct{ File settings.TelegramFile; History History }
  ```

  The commit message: the reason on the first line, a blank line, `Source: <source>`.
  `Changes` parses the `Source:` trailer from `Entry.Message`; the first line is `Reason`.

- [x] **Step 1: Write the failing tests** (`confighist_test.go`; a real `gitrepo.Repo` in
  `t.TempDir()`, skipped with `t.Skip` when `exec.LookPath("git")` fails):

  ```go
  func TestRecordCommitsTheCopyWithReasonAndSource(t *testing.T) {
  	ctx := context.Background()
  	file, repo := setup(t, "agent:\n  limits:\n    daily_turns: 200\n")
  	h := confighist.New(file, repo)
  	err := h.Record(ctx, confighist.SourcePropose, "raise the turn limit", func() error {
  		return settings.SetPath(file, []settings.Change{{Path: "agent.limits.daily_turns", Value: float64(300)}})
  	})
  	if err != nil {
  		t.Fatal(err)
  	}
  	cs, err := h.Changes(ctx, 10)
  	if err != nil || len(cs) != 1 || cs[0].Source != confighist.SourcePropose || cs[0].Reason != "raise the turn limit" {
  		t.Fatalf("Changes = %+v, %v", cs, err)
  	}
  	before, after, err := h.Versions(ctx, cs[0].ID)
  	if err != nil || len(before) != 0 || !strings.Contains(string(after), "daily_turns: 300") {
  		t.Fatalf("Versions = %q, %q, %v", before, after, err)
  	}
  }

  func TestSyncCommitsAHostEditOnce(t *testing.T) {
  	ctx := context.Background()
  	file, repo := setup(t, "log:\n  level: info\n")
  	h := confighist.New(file, repo)
  	if err := h.Sync(ctx); err != nil { // the first start records the file as it is
  		t.Fatal(err)
  	}
  	os.WriteFile(file, []byte("log:\n  level: debug\n"), 0o600)
  	if err := h.Sync(ctx); err != nil {
  		t.Fatal(err)
  	}
  	if err := h.Sync(ctx); err != nil { // nothing new
  		t.Fatal(err)
  	}
  	cs, _ := h.Changes(ctx, 10)
  	if len(cs) != 2 || cs[0].Source != confighist.SourceHost {
  		t.Fatalf("Changes = %+v", cs)
  	}
  }

  func TestAFailedWriteCommitsNothing(t *testing.T) { /* write returns an error → Record returns it, Changes is empty */ }

  func TestTwoWritersLoseNoChange(t *testing.T) {
  	// 20 goroutines, each Record setting its own module line through
  	// confighist.ModulesFile; afterwards the file has 20 lines and the
  	// copy equals the live file.
  }

  func TestNoHistory(t *testing.T) {
  	ran := false
  	h := confighist.NoHistory{}
  	if err := h.Record(context.Background(), confighist.SourceHost, "x", func() error { ran = true; return nil }); err != nil || !ran {
  		t.Fatal("NoHistory does not write")
  	}
  	if _, err := h.Changes(context.Background(), 1); !errors.Is(err, confighist.ErrNoHistory) {
  		t.Fatalf("err = %v", err)
  	}
  }
  ```

  In `files_test.go`: each wrapper's write (`SetModule`, `RemoveModule`, `SetOff`,
  `SetNotifyTo`, `SetDestination`, `RemoveDestination`, `SetGroup`, `RemoveGroup`) leaves one
  commit with its source (`module`, `destination`, `group`) and a reason that names the module,
  the destination or the group; a read (`Modules`, `Destinations`, `Groups`) commits nothing.

- [x] **Step 2: Run them, expect failures.** `go test ./confighist/`.

- [x] **Step 3: Implement.**
  - `Recorder{file string; repo Repo; mu sync.Mutex}`. `Record`: lock; `write()`; on error
    return it; then `copyAndCommit(ctx, msg)`. `copyAndCommit` reads the live file, writes it
    to `repo.Path(CopyPath)` (create `config/` with `0o750`, file `0o600`), and calls
    `repo.Commit(ctx, []string{CopyPath}, msg)`. A missing live file is copied as an empty
    file. A commit that fails after the write returns
    `fmt.Errorf("confighist: record: %w", err)`; the file stays written (Sync catches it).
  - `Sync`: lock; compare the live bytes with the copy's bytes; equal → nil; otherwise
    `copyAndCommit` with the reason `the file changed on the host` and `SourceHost`.
  - `Changes`: `repo.Log(ctx, CopyPath, n)` mapped to `Change`. `Diff`: `repo.Diff`.
    `Versions`: `FileAt(id, parent=true)` and `FileAt(id, parent=false)`; an id that `Log`
    does not know is `ErrNoChange`.
  - The wrappers: each method calls `History.Record(context.Background(), src, reason, func()
    error { return f.File.Method(args) })`. A comment says why there is no caller context: the
    writers' interfaces take none, and git runs apart from the caller anyway (ruling R37).
    Reasons: `module <name>: settings`, `module <name>: off`, `module <name>: on`,
    `module <name>: notify_to`, `module <name>: removed`, `destination <name>: set`,
    `destination <name>: removed`, `group <name or chat>: connected`, `group <chat>: disconnected`.
  - `telemetry.go`: counter `ganglion.config.changes` with the label `source` (a closed set),
    and a log line per recorded change (source, reason, commit id) — conventions, rule 11.

- [x] **Step 4: Run.** `go test -race ./confighist/` — PASS.

- [x] **Step 5: Commit.** `confighist: record every change of the node's file in the workspace`
  with `Refs #236`.

### Task 4: wiring in `config`

**Files:**
- Create: `config/confighist.go`
- Modify: `config/config.go` (register the provider), `config/groups.go:26,64,75`,
  `config/modhost.go:49,110`
- Test: `config/confighist_test.go`; the existing container test

**Interfaces:**
- Consumes: `confighist.New`, `NoHistory`, the wrappers (Task 3); `Workspace()` provider
  (`config/memory.go:35`); `settingsFile(b)` (`config/modhost.go:72`).
- Produces: `func ConfigHistory() do.Provider[confighist.History]`.

- [x] **Step 1: Write the failing test.** In `config/confighist_test.go`, build the agent
  role's container over a temporary data directory with a `ganglion.yaml`, invoke
  `confighist.History`, and check that the workspace has `config/ganglion.yaml` equal to the
  live file and one commit with `Source: host` (the start's `Sync`). A second test: with a
  workspace where git is missing (set `PATH` to an empty directory with `t.Setenv`), the
  provider returns `confighist.NoHistory{}` and the node still builds.

- [x] **Step 2: Run it, expect a failure.** `go test ./config/ -run ConfigHistory`.

- [x] **Step 3: Implement.**

  ```go
  // ConfigHistory is the record of the node's file (R-CFG-13, ADR-0093). It
  // commits a host edit at start; with no git it keeps no history.
  func ConfigHistory() do.Provider[confighist.History] {
  	return func(i do.Injector) (confighist.History, error) {
  		ws, err := do.Invoke[*gitrepo.Repo](i)
  		if errors.Is(err, gitrepo.ErrNoGit) {
  			return confighist.NoHistory{}, nil
  		}
  		if err != nil {
  			return nil, err
  		}
  		h := confighist.New(settingsFile(do.MustInvoke[Bootstrap](i)), ws)
  		return h, h.Sync(do.MustInvoke[Startup](i))
  	}
  }
  ```

  Register it with `do.Lazy(ConfigHistory())` beside `Workspace()`. Replace each inline
  `settings.ModulesFile{...}`, `settings.DestinationsFile{...}` and the `GroupsFile()`
  provider's value with the `confighist` wrappers; `GroupsFile` now provides
  `confighist.TelegramFile`, so update its consumers' types (`groups.go:37,48,56,76`). The
  consumers take interfaces, so only the construction changes. Invoke `confighist.History`
  once in the agent role's start, next to where the workspace is first used, so `Sync` runs at
  every start and not only when a tool is first built.

- [x] **Step 4: Run.** `go test -race ./config/... ./settings/... ./confighist/...` — PASS.

- [x] **Step 5: Commit.** `config: every writer of the node's file goes through its history`
  with `Refs #236`.

### Task 5: `config.propose`, `config.history`, `config.rollback`, the restart button

**Files:**
- Create: `tool/builtin/config.go` (tools), `tool/builtin/config_restart.go` (the button)
- Modify: `texts/texts.go`, `texts/lang/en.yaml`, `texts/lang/ru.yaml`, `texts/lang/uk.yaml`;
  `config/agent.go` (`toolHandlers`, `quickActions`); `config/confighist.go` (a `Configuring`
  provider)
- Test: `tool/builtin/config_test.go`

**Interfaces:**
- Consumes: `settings.SetPath`, `Apply`, `Values`, `Allowed`, `Where`, `Change`, `ErrNotAllowed` (Task 1);
  `confighist.History`, `SourcePropose`, `SourceRollback`, `ErrNoHistory`, `ErrNoChange`
  (Task 3); `ButtonStore` (`tool/builtin/memory_note.go:61`); `pending.Button`,
  `pending.QuickAction`, `pending.QuickPress`, `pending.QuickEdit`; `agent.RestartFunc`
  (converted in `config`).
- Produces:

  ```go
  const (
  	ConfigProposeTool  = "config.propose"
  	ConfigHistoryTool  = "config.history"
  	ConfigRollbackTool = "config.rollback"
  	ActionConfigRestart = "config.restart"
  )

  // RestartFunc ends the node for its supervisor (the /restart path).
  type RestartFunc func()

  type Configuring struct {
  	File    string               // the node's file
  	History confighist.History
  	Buttons ButtonStore
  	Restart RestartFunc
  	Now     func() time.Time
  }
  func (c Configuring) Tools() []tool.Handler
  func (c Configuring) QuickActions() map[string]pending.QuickAction
  ```

  Arguments (strict JSON, unknown fields refused):
  - `config.propose`: `{"changes":[{"path":"agent.limits.daily_turns","value":300}],"reason":"..."}`;
    1–10 changes, distinct paths, `reason` 1–300 runes.
  - `config.history`: `{"limit":10}` (1–20, default 10).
  - `config.rollback`: `{"change":"<id>"}` — optional; the latest change when absent.

- [x] **Step 1: Write the failing tests** (`config_test.go`; a temp file, a fake `History`
  that records `Record` calls and returns canned `Changes`/`Versions`, a fake `ButtonStore`,
  a `Restart` that counts):

  ```go
  func TestProposeRefusesAFieldOffTheList(t *testing.T) {
  	c := newConfiguring(t, "telegram:\n  rich: true\n")
  	call := adminCall(`{"changes":[{"path":"sandbox.deny","value":[]}],"reason":"x"}`)
  	_, err := question(c, builtin.ConfigProposeTool, call)
  	if !errors.Is(err, settings.ErrNotAllowed) || !strings.Contains(err.Error(), "host") {
  		t.Fatalf("err = %v, want ErrNotAllowed naming the host", err)
  	}
  }

  func TestProposeRefusesABadValueBeforeTheCard(t *testing.T) {
  	c := newConfiguring(t, "")
  	_, err := question(c, builtin.ConfigProposeTool, adminCall(`{"changes":[{"path":"agent.limits.daily_turns","value":"many"}],"reason":"x"}`))
  	if err == nil || !strings.Contains(err.Error(), "agent.limits.daily_turns") {
  		t.Fatalf("err = %v", err)
  	}
  }

  func TestProposeQuestionShowsOldAndNewAndTaint(t *testing.T) {
  	c := newConfiguring(t, "agent:\n  limits:\n    daily_turns: 200\n")
  	call := adminCall(`{"changes":[{"path":"agent.limits.daily_turns","value":300}],"reason":"more room"}`)
  	call.Tainted = true
  	q, err := question(c, builtin.ConfigProposeTool, call)
  	if err != nil {
  		t.Fatal(err)
  	}
  	for _, want := range []string{"agent.limits.daily_turns", "200", "300", "more room", "outside"} {
  		if !strings.Contains(q, want) {
  			t.Errorf("question lacks %q:\n%s", want, q)
  		}
  	}
  }

  func TestProposeRunWritesThroughTheHistoryAndOffersRestart(t *testing.T) {
  	c, h, b := newConfiguringWithFakes(t, "agent:\n  limits:\n    daily_turns: 200\n")
  	call := adminCall(`{"changes":[{"path":"agent.limits.daily_turns","value":300}],"reason":"more room"}`)
  	call.Approved = true
  	res, err := run(c, builtin.ConfigProposeTool, call)
  	if err != nil {
  		t.Fatal(err)
  	}
  	if h.records[0].src != confighist.SourcePropose || h.records[0].reason != "more room" {
  		t.Fatalf("records = %+v", h.records)
  	}
  	if !strings.Contains(readFile(t, c.File), "daily_turns: 300") {
  		t.Fatal("not written")
  	}
  	if len(res.Out) != 1 || len(res.Out[0].Buttons) != 1 || b.added[0].Action != builtin.ActionConfigRestart {
  		t.Fatalf("no restart button: %+v", res.Out)
  	}
  }

  func TestProposeRunChecksAgainstTheFileAsItIsNow(t *testing.T) {
  	// The question passed; then the file is made such that the change no
  	// longer passes (e.g. models.purposes names a model removed meanwhile).
  	// Run refuses with the path and writes nothing.
  }

  func TestRollbackProposesTheEarlierValuesOfAllowedFieldsOnly(t *testing.T) {
  	// Versions(id): before = "agent:\n  limits:\n    daily_turns: 200\nsandbox:\n  hide_names: true\n"
  	//               after  = "agent:\n  limits:\n    daily_turns: 300\nsandbox:\n  hide_names: false\n"
  	// The question names daily_turns 300 → 200 and lists sandbox.hide_names
  	// as "not rolled back here" with the host as its place; Run writes
  	// daily_turns: 200 with SourceRollback and leaves sandbox alone.
  }

  func TestHistoryWithoutGitSaysSo(t *testing.T) {
  	// History = confighist.NoHistory{} → config.history's result text says
  	// there is no history, no error is returned to the model.
  }

  func TestRestartButtonRestarts(t *testing.T) {
  	// QuickActions()[ActionConfigRestart] with a press → Restart called once,
  	// the edit's text is texts.ReplyRestarting.
  }

  func TestConfigToolsAreAdminOnlyAndNotUnderAGrant(t *testing.T) {
  	// Spec().AdminOnly for all three; a Call with Grant != nil is refused by
  	// Question and Run.
  }
  ```

  Fill in the bodies marked with comments in the same style as the first four; they are
  behaviour tests through `Question` and `Run`.

- [x] **Step 2: Run them, expect failures.** `go test ./tool/builtin/ -run Config`.

- [x] **Step 3: Implement.**
  - Follow `routeTool` in `tool/builtin/destinations.go:162-245` for the confirmed tools
    (`Class: tool.ClassSideEffect, Policy: tool.PolicyConfirm, AdminOnly: true`, the
    `allowed` check refusing a grant, `whole(name, q)` for the question length).
    `config.history` is `Class: tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true`.
  - A shared `check(file string, cs []settings.Change) (old map[string]any, err error)`:
    every path `settings.Allowed`, else
    `fmt.Errorf("%w: %s is changed %s", settings.ErrNotAllowed, path, placeWords(settings.Where(path)))`;
    then `settings.Apply(current, cs)` on the file's bytes (it parses the result); `old` comes
    from `settings.Values(current, paths)`.
  - Question: one line per change, `path: old → new` (a missing old value is shown as
    `default`), then the reason, then, when `c.Tainted`, the line of `texts.AskConfigTainted`.
  - Run: `check` again against the file as it is now; `History.Record(ctx, SourcePropose,
    reason, func() error { return settings.SetPath(file, cs) })`; the result text for the model
    names each field's value just before the write; `Out` is one message with the text
    `texts.ConfigWritten` and a `config.restart` button (TTL 24 h, the principal and the
    conversation of the call), added through `Buttons.AddButtons` as `memory_note.go:113` does.
  - Rollback: `History.Changes` for the latest id when none is given; `Versions(id)`;
    `settings.Values` of both for `settings.AllowedPaths`; changed allowed paths become
    `[]settings.Change` with the `before` values (`nil` where the key was missing before);
    paths that differ outside the allowlist are found by comparing the top-level keys'
    `Values` of both versions and listed with `placeWords(settings.Where(path))`; no allowed
    path changed → a result that says so, no card. Then the same `check`, question and run as
    a proposal, with `SourceRollback` and the reason `roll back <short id>: <its reason>`.
  - History: `Changes(limit)` and `Diff` for each, formatted as date, source, reason, diff.
    The result is not outside content (`External` stays false): it is the node's own file, and
    every line in it passed an owner's press or was written on the host.
    `ErrNoHistory` becomes a result text, not an error.
  - The button's action calls `Restart()` and returns
    `pending.QuickEdit{Text: p.Speak.Text(texts.ReplyRestarting, nil), Reviewed: true}`.
  - Texts (three languages; `allKeys`): `ask.config_propose` (vars: lines, reason),
    `ask.config_rollback` (vars: lines, reason, kept), `ask.config_tainted`,
    `reply.config_written`, `button.config_restart`.
  - `config/agent.go`: add `do.MustInvoke[builtin.Configuring](i).Tools()` to the
    `slices.Concat` in `toolHandlers` and `maps.Copy(out, ...QuickActions())` in
    `quickActions`. The `Configuring` provider in `config/confighist.go` takes `File:
    settingsFile(b)`, `History`, the pending store as `ButtonStore`, `Restart:
    builtin.RestartFunc(do.MustInvoke[agent.RestartFunc](i))` — invoked lazily so the
    override in `Run` (`config/config.go:73`) is the one used — and `Now`.

- [x] **Step 4: Run.** `go test -race ./tool/builtin/ ./texts/ ./config/...` — PASS.

- [x] **Step 5: Commit.** `tool: config.propose, config.history, config.rollback and the restart button`
  with `Refs #236`.

### Task 6: chatlab scenarios

**Files:**
- Create: `tools/chatlab/scenarios/mech-config-propose.yaml`,
  `tools/chatlab/scenarios/mech-config-rollback.yaml`,
  `tools/chatlab/scenarios/mech-config-refused.yaml`
- Modify (only if a fact is missing): `internal/chatlab/facts.go`,
  `schemas/chatlab-scenario.schema.json`

- [x] **Step 1: Write the scenarios** (scripted model; follow
  `tools/chatlab/scenarios/mech-write-tainted-approved.yaml`):
  - `mech-config-propose`: `config: {agent: {limits: {daily_turns: 200}}}`; script
    `{tool: config.propose, args: '{"changes":[{"path":"agent.limits.daily_turns","value":300}],"reason":"more room"}'}`
    then a `say`; steps: say "raise the daily turn limit to 300" → `facts: {questions: 1,
    question: {contains: "300"}}` → `press: Apply` → `facts: {journal: [{tool:
    config.propose, decision: approved, result_contains: "300"}]}`.
  - `mech-config-rollback`: the same change applied, then "roll it back": script
    `config.rollback` with no arguments → a card with `300 → 200` → press → the journal shows
    the rollback approved.
  - `mech-config-refused`: script `config.propose` with `sandbox.deny` → `facts: {questions:
    0, journal: [{tool: config.propose, err_contains: "host"}]}`.
- [x] **Step 2: Run them.** `make lab LAB=mech-config-propose`, then the other two. If the
  journal facts cannot show the file was written, add a `config` fact (the value at a path of
  the node's `ganglion.yaml`) to `facts.go` and the scenario schema, with a test.
- [x] **Step 3: Run the shipped scripted scenarios test.**
  `go test -race -tags lab ./internal/chatlab/... -run TestShippedScriptedScenariosPass`.
- [x] **Step 4: Commit.** `chatlab: scenarios for config.propose, rollback and a refusal`
  with `Refs #236`.

### Task 7: documents

**Files:**
- Modify: `docs/architecture/05-code-map.md` (`confighist`; `settings` and `gitrepo` lines),
  `docs/plans/p1-all-in-one.md` (rows 6.1–6.3), `docs/README.md` ("Where we are"),
  `docs/plans/p1-m6-config.md` (tick the tasks)
- Create: `docs/guide/using/settings.md`; modify `mkdocs.yml` (`nav`, after "Commands") and
  `docs/guide/using/commands.md` (a line that points to the new page)

- [x] **Step 1: Code map.** Add `confighist` with one line on what it is and who uses it;
  note `settings.SetPath` / the allowlist and `gitrepo`'s history reads.
- [x] **Step 2: Guide page** `docs/guide/using/settings.md`, for someone running a node: ask
  the agent in words ("raise the daily limit to 300 turns"); a card shows the old and new value;
  Apply writes, the change works after a restart, "Restart now" does it; "what changed in the
  settings?" lists the history; "roll that back"; the list of what can be changed from the chat
  (the R-CFG-14 fields in plain words); what stays on the host and why (secrets, the admin,
  model providers, the sandbox, the public address). No spec ids, no ADR talk; placeholders
  only. Then `mkdocs.yml`'s `nav`.
- [x] **Step 3: Phase plan and status.** Mark 6.1–6.3 built (the date, the PR) in
  `p1-all-in-one.md`; "Where we are" in `docs/README.md` says what was built and that the live
  check follows.
- [x] **Step 4: Commit.** `docs: the node's file through the agent — code map, guide, status`
  with `Refs #236`.

### Task 8: the pull request and the live check

- [x] **Step 1:** `make check` and the three scenarios once more; open the pull request with
  `Fixes #236` only after the live check passed, else `Refs #236`.
- [x] **Step 2: Live check on Railway** (owner's node, after a release and `/update`; nothing
  about the node goes into the repository): ask for a change of `agent.limits.daily_turns`;
  Apply; "Restart now"; `/status` or the next turn shows the new limit; "what changed in the
  settings?" lists it; roll it back; restart; the old value holds. Record what was done and
  seen in the PR, without the node's address.
- [x] **Step 3:** close #236 when the live check passed; check it reached Done on the board.
