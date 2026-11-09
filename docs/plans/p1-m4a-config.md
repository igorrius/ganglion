# P1 · M4a tasks 4.1–4.2: one config file and models by purpose — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development
> (recommended) or superpowers:executing-plans to implement this plan task by task.
> Steps use checkbox (`- [ ]`) syntax.

**Goal:** the node reads everything an operator chooses from one file, `ganglion.yaml`, checked
strictly against a schema that describes every field. Secrets appear in it only as `env:NAME`.
The old `GANGLION_*` settings and the models file fail the start with the key that replaces them.
Models are chosen by purpose (`chat`, `check`, `compact`, `translate`, `code`), each an ordered
list with an optional reasoning effort. The node sets `GOMEMLIMIT` from the file or the
container's limit. `/restart` ends the node for its supervisor to start again.

**Architecture:**

- **New packages:**
  - `settings` is the file: its types, defaults, strict loading, `env:NAME` references, the
    check against retired variables, and the `models:` section with purposes. It imports
    `router` (types only) and `secrets`, nothing that does I/O beyond reading the file.
  - `internal/size` parses "100MiB"-style sizes; `runner` moves to it (one source).
  - `internal/memlimit` decides the soft memory limit: the configured value, else 80 % of the
    cgroup v2 `memory.max` / v1 `memory.limit_in_bytes`, else none.
- **Existing parts that change:**
  - `cmd/flags` keeps `--config`, `--db`, `--roles` (and `ganglion mcp`'s flags); every other
    `run` flag goes. `cmd/run.go` builds `config.Bootstrap` from `settings.File`.
  - `router` gets `Purpose` (replacing `TaskKind` and `Tier`), `Effort`, and `Candidate.Effort`;
    `router/rules` routes by the purpose's ordered list; `rules.Catalog` and `LoadCatalog` move
    into `settings`.
  - `provider.ChatRequest` gets `Effort`; `provider/openai` sends `reasoning_effort`;
    `provider/claudecli` passes `--effort`.
  - Skills' frontmatter `model.tier` becomes `model.purpose`.
  - The agent gets `/restart`; `main.go` exits with code 75 after a clean restart stop.
  - The lab, `make accept`, the live targets, `ganglion mcp`'s `config.show`, `tools/chatlab`
    and `tools/modelcompare` read example config files.

**Tech stack:** Go 1.27, `go.yaml.in/yaml/v3` (already direct), urfave/cli v3. No new module.

**Spec:** 7.14 (R-CFG-1..7), 7.3 (R-RTR-6), R-RUN-6; the [M4 design](p1-m4-reflexes-design.md)
sections 4–5; [ADR-0059](../adr/0059-node-config-file.md) and
[ADR-0060](../adr/0060-models-by-purpose.md) (Proposed, accepted when M4 closes),
[ADR-0023](../adr/0023-json-schemas-for-yaml-files.md), [ADR-0056](../adr/0056-small-hosts-as-a-soft-target.md).
Read them with this plan. M4b (modules, cells, the runner, the builder, Deye, deploy) gets its own
plan once this one is done; the `modules:` section is parsed here and used there.

## Global Constraints

- All documentation, comments and doc comments are in English; the [conventions](../conventions.md)
  apply (constructors, no `nil` collaborators, named types for closed sets, small functions, the
  linter's limits, an injected clock, no sleeping in tests).
- Work on branch `m4-reflexes`. Commit after each task; `make lint test` green before each commit.
- Architecture docs change in the same commit as the code (`05-code-map.md` with its dependency
  rules; `03-components.md` when components connect differently).
- Only `config` builds and connects components; domain packages never import `samber/do`. Only
  `cmd/...` imports urfave/cli. Domain packages never read environment variables: `settings`
  reads them only through an injected `Getenv`.
- **A secret never appears in the file, a log line or an error.** Errors name the path and the
  variable, never a value.
- **Fail closed at start:** an unknown key, a literal secret, a retired variable, an unknown
  purpose, a purpose naming an unknown model, an effort the provider cannot take — each stops
  the start with `ganglion.yaml: <path>: <reason>`.
- `OTEL_EXPORTER_OTLP_ENDPOINT` (and the other `OTEL_*`) stay read by OpenTelemetry as today.

## Rulings this plan makes (call; the owner may overturn them in review)

1. **A fifth purpose, `translate`.** `language/translate.go` calls a model to translate fixed
   texts (R-LANG-5); the design's four purposes miss it. It gets its own list.
2. **Defaults between purposes.** `chat` is required when `models:` is present. `check`,
   `compact` and `translate` use `chat`'s list when absent. `code` has no default: absent means
   "no model to write code" (P3).
3. **The budget.** Over the daily budget, `chat` keeps only free (`price_in` and `price_out` 0) or
   local models; the other purposes are housekeeping and are not cut. Today paid cheap models
   kept answering over budget; with purposes there is no "cheap" to fall to. With the claude CLI
   (price 0) nothing changes.
4. **Cooling.** A cooling model moves to the end of its purpose's list (stable), as it moved to
   the back of its tier before.
5. **Efforts.** The closed set is `low`, `medium`, `high`, `xhigh`, `max` (the claude CLI's).
   OpenAI-compatible providers take `low`, `medium`, `high`; a higher one fails the start.
6. **`TaskKind` folds into `Purpose`.** The metric attribute `task` keeps its name and now carries
   the purpose (`message` → `chat`, `compaction` → `compact`, `guard` → `check`).
7. **Default secret references.** Without the keys, `telegram.token` is `env:TELEGRAM_BOT_TOKEN`
   and `agent.search.brave_key` is `env:BRAVE_API_KEY`, so a node with no file behaves as a node
   with no variables does today. An unset variable means "off", as today.
8. **`/restart` exit code.** The node stops cleanly and exits with 75 (`EX_TEMPFAIL`), so a
   supervisor that restarts only on failure (Railway's default) starts it again.
9. **Model ids.** A model in `models.models` has an `id`, unique, used by the purposes; when absent
   it is the model's `name`.

## Review Focus

1. **A secret leaks** into the file, an error, a log line or `config.show`.
2. **A retired variable is silently ignored** on a deploy (Railway keeps old variables).
3. **The order of a purpose's list is not the order tried**, or a cooling model blocks.
4. **The schema and the loader drift apart.**
5. **`GOMEMLIMIT` on a container sized for builds** takes 800 MB unless the file sets it.

## Files

| File | Change |
|---|---|
| `internal/size/size.go` (+test) | new: `Parse` |
| `runner/manifest.go` | uses `size.Parse` |
| `internal/memlimit/memlimit.go` (+test) | new: `Decide`, `Apply` |
| `settings/settings.go` | new: `File` and sections, `Default` |
| `settings/types.go` | new: `SecretRef`, `Duration`, `Count`, `Toggle`, `Size` YAML types |
| `settings/load.go` | new: `Load`, `Parse`, errors with paths |
| `settings/retired.go` | new: the retired variables and their keys |
| `settings/models.go` | new: providers, models, purposes, efforts; `Router` |
| `settings/*_test.go`, `settings/schema_test.go` | new |
| `schemas/node.schema.json` | new; `schemas_test.go` lists it |
| `schemas/models.schema.json`, `router/rules/catalog*.go` | removed |
| `router/router.go` | `Purpose`, `Effort`, `TaskReq.Purpose`, `Candidate.Effort`; `Tier`, `TaskKind`, `Model.Tier` removed |
| `router/rules/rules.go` (+test) | routes by purpose |
| `provider/provider.go`, `provider/openai/client.go`, `provider/claudecli/claudecli.go` (+tests) | `Effort` |
| `agent/stages.go`, `agent/compact.go`, `agent/status.go`, `agent/model.go`, `agent/skills.go`, `guard/guard.go`, `language/translate.go` | purposes |
| `skills/skills.go` (+test) | `model.purpose` |
| `agent/commands.go`, `agent/restart.go` (+test), `agent/deps.go` or the `Deps` struct | `/restart` |
| `node/restart.go`, `main.go` | `ErrRestart`, exit 75 |
| `cmd/flags/flags.go`, `cmd/run.go`, `cmd/mcp.go` (+tests) | `--config`; bootstrap from settings |
| `config/bootstrap.go`, `config/agent.go`, `config/lab.go`, `config/accept_test.go` | settings types |
| `internal/chatlab/*`, `tools/chatlab/main.go`, `tools/modelcompare/main.go` | config files |
| `examples/ganglion.example.yaml`, `examples/ganglion.lab.yaml` | new |
| `models.example.yaml`, `tools/chatlab/haiku-only.yaml` | replaced by examples |
| `Makefile`, `.env.example`, `README.md`, `docs/README.md` | config files |
| `docs/architecture/05-code-map.md`, `03-components.md` | new packages |

---

### Task 1: `internal/size` and `internal/memlimit` (R-RUN-6, R-CFG-7)

**Files:**
- Create: `internal/size/size.go`, `internal/size/size_test.go`
- Modify: `runner/manifest.go:108-125` (the private `parseSize` goes; callers use `size.Parse`)
- Create: `internal/memlimit/memlimit.go`, `internal/memlimit/memlimit_test.go`

**Interfaces:**
- Produces: `size.Parse(s string) (uint64, error)`; `memlimit.Decide(configured uint64, read ReadFunc) Limit`;
  `memlimit.Apply(l Limit) Limit`; `type Limit struct{ Bytes int64; Source Source }`;
  `type Source string` with `SourceConfig`, `SourceCgroup`, `SourceNone`;
  `type ReadFunc func(path string) ([]byte, error)`.

- [x] **Step 1: Write the failing tests**

```go
// internal/size/size_test.go
package size_test

import (
	"testing"

	"github.com/igorrius/ganglion/internal/size"
)

func TestParse(t *testing.T) {
	for in, want := range map[string]uint64{"0": 0, "512": 512, "64KiB": 64 << 10, "100MiB": 100 << 20, "1GiB": 1 << 30} {
		got, err := size.Parse(in)
		if err != nil || got != want {
			t.Errorf("Parse(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "MiB", "-1MiB", "1.5MiB", "10MB", "1TiB"} {
		if _, err := size.Parse(bad); err == nil {
			t.Errorf("Parse(%q): want an error", bad)
		}
	}
}
```

```go
// internal/memlimit/memlimit_test.go
package memlimit_test

import (
	"errors"
	"io/fs"
	"testing"

	"github.com/igorrius/ganglion/internal/memlimit"
)

func files(m map[string]string) memlimit.ReadFunc {
	return func(p string) ([]byte, error) {
		if v, ok := m[p]; ok {
			return []byte(v), nil
		}
		return nil, fs.ErrNotExist
	}
}

func TestDecide(t *testing.T) {
	cases := []struct {
		name       string
		configured uint64
		fs         map[string]string
		want       memlimit.Limit
	}{
		{"configured wins", 100 << 20, map[string]string{memlimit.CgroupV2: "268435456\n"}, memlimit.Limit{Bytes: 100 << 20, Source: memlimit.SourceConfig}},
		{"cgroup v2 at 80 percent", 0, map[string]string{memlimit.CgroupV2: "268435456\n"}, memlimit.Limit{Bytes: 214748364, Source: memlimit.SourceCgroup}},
		{"cgroup v2 max means none", 0, map[string]string{memlimit.CgroupV2: "max\n"}, memlimit.Limit{Source: memlimit.SourceNone}},
		{"cgroup v1", 0, map[string]string{memlimit.CgroupV1: "268435456\n"}, memlimit.Limit{Bytes: 214748364, Source: memlimit.SourceCgroup}},
		{"v1 unlimited is a huge number", 0, map[string]string{memlimit.CgroupV1: "9223372036854771712\n"}, memlimit.Limit{Source: memlimit.SourceNone}},
		{"nothing", 0, nil, memlimit.Limit{Source: memlimit.SourceNone}},
		{"garbage", 0, map[string]string{memlimit.CgroupV2: "lots\n"}, memlimit.Limit{Source: memlimit.SourceNone}},
	}
	for _, c := range cases {
		if got := memlimit.Decide(c.configured, files(c.fs)); got != c.want {
			t.Errorf("%s: Decide = %+v, want %+v", c.name, got, c.want)
		}
	}
}

func TestDecideReadErrorIsNone(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, errors.New("denied") }
	if got := memlimit.Decide(0, read); got.Source != memlimit.SourceNone {
		t.Fatalf("got %+v", got)
	}
}
```

The 256 MB case is the phase plan's check: 80 % of 268,435,456 is 214,748,364 bytes, about 205 MiB.

- [x] **Step 2: Run the tests to see them fail**

Run: `go test ./internal/size/ ./internal/memlimit/`
Expected: FAIL, the packages do not exist.

- [x] **Step 3: Implement**

```go
// internal/size/size.go

// Package size parses byte sizes written as "8MiB", "64KiB", "1GiB" or
// plain bytes, the one form manifests and the node config share.
package size

import (
	"errors"
	"strconv"
	"strings"
)

var units = []struct {
	suffix string
	mult   uint64
}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}}

// Parse reads s; a bare number is bytes.
func Parse(s string) (uint64, error) {
	num, mult := s, uint64(1)
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			num, mult = strings.TrimSuffix(s, u.suffix), u.mult
			break
		}
	}
	n, err := strconv.ParseUint(num, 10, 64)
	if err != nil {
		return 0, errors.New("size " + strconv.Quote(s) + ": want a whole number of bytes, KiB, MiB or GiB")
	}
	if n > ^uint64(0)/mult {
		return 0, errors.New("size " + strconv.Quote(s) + ": too large")
	}
	return n * mult, nil
}
```

```go
// internal/memlimit/memlimit.go

// Package memlimit decides the node's soft memory limit (R-RUN-6,
// ADR-0056): the configured value, else about 80 % of the container's
// limit, else none.
package memlimit

import (
	"runtime/debug"
	"strconv"
	"strings"
)

// The cgroup files that hold the container's memory limit.
const (
	CgroupV2 = "/sys/fs/cgroup/memory.max"
	CgroupV1 = "/sys/fs/cgroup/memory/memory.limit_in_bytes"
)

// share is the part of the container's limit the heap may use.
const share = 0.8

// unlimitedV1 is above any real limit: cgroup v1 writes a page-aligned
// maximum when there is none.
const unlimitedV1 = 1 << 62

// Source tells where a limit came from.
type Source string

// Sources.
const (
	SourceConfig Source = "config"
	SourceCgroup Source = "cgroup"
	SourceNone   Source = "none"
)

// Limit is the decided limit; Bytes is 0 when Source is SourceNone.
type Limit struct {
	Bytes  int64
	Source Source
}

// ReadFunc reads a file, as os.ReadFile does.
type ReadFunc func(path string) ([]byte, error)

// Decide returns the configured bytes when set, else 80 % of the cgroup
// limit, else none. A file it cannot read or parse counts as no limit.
func Decide(configured uint64, read ReadFunc) Limit {
	if configured > 0 {
		return Limit{Bytes: int64(configured), Source: SourceConfig}
	}
	for _, p := range []string{CgroupV2, CgroupV1} {
		if n, ok := cgroup(read, p); ok {
			return Limit{Bytes: int64(float64(n) * share), Source: SourceCgroup}
		}
	}
	return Limit{Source: SourceNone}
}

func cgroup(read ReadFunc, path string) (uint64, bool) {
	b, err := read(path)
	if err != nil {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(b)), 10, 64)
	if err != nil || n == 0 || n >= unlimitedV1 {
		return 0, false
	}
	return n, true
}

// Apply sets the limit on the runtime unless it is none, and returns it.
func Apply(l Limit) Limit {
	if l.Source != SourceNone {
		debug.SetMemoryLimit(l.Bytes)
	}
	return l
}
```

In `runner/manifest.go`, delete `parseSize` and call `size.Parse` where it was called (the
`limits.memory` field); keep the error wrapping the caller already adds.

- [x] **Step 4: Run the tests**

Run: `go test ./internal/size/ ./internal/memlimit/ ./runner/`
Expected: PASS.

- [x] **Step 5: Docs and commit**

Add `internal/size` and `internal/memlimit` to `docs/architecture/05-code-map.md` (the
`internal/` list: "byte sizes" and "the soft memory limit, R-RUN-6").

```bash
git add internal/size internal/memlimit runner/manifest.go docs/architecture/05-code-map.md
git commit -m "size, memlimit: one size parser; the soft memory limit from config or cgroup (R-RUN-6)"
```

---

### Task 2: `settings` — the file, its types, strict loading, `env:NAME` (4.1; R-CFG-1..4)

**Files:**
- Create: `settings/settings.go`, `settings/types.go`, `settings/load.go`
- Test: `settings/types_test.go`, `settings/load_test.go`

**Interfaces:**
- Consumes: `secrets.Source` (`Secret(name string) (string, error)`), `size.Parse`.
- Produces (later tasks rely on these exact names):

```go
type File struct {
	Node     Node     `yaml:"node"`
	Log      Log      `yaml:"log"`
	Metrics  Metrics  `yaml:"metrics"`
	Telegram Telegram `yaml:"telegram"`
	Agent    Agent    `yaml:"agent"`
	Sandbox  Sandbox  `yaml:"sandbox"`
	Models   *Models  `yaml:"models"`  // nil: the agent is off (task 3)
	Modules  []Module `yaml:"modules"` // used in M4b
}
type Node struct {
	MemoryLimit Size   `yaml:"memory_limit"`
	TZ          string `yaml:"tz"`
}
type Log struct {
	Level string `yaml:"level"` // debug | info | warn | error
	Dir   string `yaml:"dir"`   // "" = logs beside the database; "off"
}
type Metrics struct {
	Addr string `yaml:"addr"` // "off" or host:port
}
type Telegram struct {
	Token     SecretRef `yaml:"token"`
	APIURL    string    `yaml:"api_url"`
	Admin     string    `yaml:"admin"`
	Reply     string    `yaml:"reply"` // auto | always | off
	Rich      bool      `yaml:"rich"`
	Reactions Reactions `yaml:"reactions"`
}
type Reactions struct {
	After Duration `yaml:"after"` // off = 0
	Dwell Duration `yaml:"dwell"`
}
type Agent struct {
	Workspace   string   `yaml:"workspace"`
	Languages   string   `yaml:"languages"` // "" = languages beside the database
	SessionIdle Duration `yaml:"session_idle"`
	Limits      Limits   `yaml:"limits"`
	Search      Search   `yaml:"search"`
}
type Limits struct {
	DailyBudgetUSD Money `yaml:"daily_budget_usd"`
	DailyTurns     Count `yaml:"daily_turns"`
	DailyTokens    Count `yaml:"daily_tokens"`
}
type Search struct {
	Mode     string    `yaml:"mode"` // auto | off
	BraveKey SecretRef `yaml:"brave_key"`
}
type Sandbox struct {
	Deny      []string `yaml:"deny"`
	HideNames bool     `yaml:"hide_names"`
}
type Module struct {
	Name   string         `yaml:"name"`
	Config map[string]any `yaml:"config"`
	Off    bool           `yaml:"off"`
}

func Default() File
func Parse(src []byte) (File, error)         // strict; errors "ganglion.yaml: <path>: <reason>"
func Load(path string, read ReadFunc) (File, error) // a missing file is Default()
type ReadFunc func(path string) ([]byte, error)
type SecretRef string                          // "env:NAME"; Name() string; Resolve(src secrets.Source) (string, error)
type Duration time.Duration                    // "6h", "off" = 0
type Count int                                 // whole number ≥ 0, "off" = 0
type Money float64                             // ≥ 0, "off" = 0
type Size uint64                               // size.Parse form; 0 = unset
```

`Limits` accepts the scalar `off` (every limit 0) as well as a mapping.

- [x] **Step 1: Write the failing tests**

```go
// settings/types_test.go
package settings_test

import (
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

func TestSecretRefRejectsLiterals(t *testing.T) {
	_, err := settings.Parse([]byte("telegram:\n  token: 123:ABC\n"))
	if err == nil || !strings.Contains(err.Error(), "telegram.token") || strings.Contains(err.Error(), "123:ABC") {
		t.Fatalf("err = %v; want a path and no value", err)
	}
}

func TestSecretRefResolves(t *testing.T) {
	f, err := settings.Parse([]byte("telegram:\n  token: env:BOT\n"))
	if err != nil {
		t.Fatal(err)
	}
	src := fakeSource{"BOT": "t0k"}
	got, err := f.Telegram.Token.Resolve(src)
	if err != nil || got != "t0k" {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
	if f.Telegram.Token.Name() != "BOT" {
		t.Fatalf("Name = %q", f.Telegram.Token.Name())
	}
}

func TestOffAndDefaults(t *testing.T) {
	f, err := settings.Parse([]byte("agent:\n  limits: off\n  session_idle: off\nmetrics: {addr: off}\n"))
	if err != nil {
		t.Fatal(err)
	}
	l := f.Agent.Limits
	if l.DailyTurns != 0 || l.DailyTokens != 0 || l.DailyBudgetUSD != 0 || f.Agent.SessionIdle != 0 || f.Metrics.Addr != "off" {
		t.Fatalf("got %+v, idle %v", l, f.Agent.SessionIdle)
	}
	d, err := settings.Parse([]byte("agent:\n  limits: {daily_turns: off}\n"))
	if err != nil {
		t.Fatal(err)
	}
	def := settings.Default().Agent.Limits
	if d.Agent.Limits.DailyTurns != 0 || d.Agent.Limits.DailyTokens != def.DailyTokens || d.Agent.Limits.DailyBudgetUSD != def.DailyBudgetUSD {
		t.Fatalf("one off, the rest default: got %+v", d.Agent.Limits)
	}
}

type fakeSource map[string]string

func (f fakeSource) Secret(name string) (string, error) { return f[name], nil }
```

```go
// settings/load_test.go
package settings_test

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/settings"
)

func TestUnknownKeyNamesThePath(t *testing.T) {
	_, err := settings.Parse([]byte("agent:\n  limits:\n    daily_turnz: 3\n"))
	if err == nil || !strings.Contains(err.Error(), "agent.limits") || !strings.Contains(err.Error(), "daily_turnz") {
		t.Fatalf("err = %v", err)
	}
}

func TestBadValuesNameThePath(t *testing.T) {
	for src, path := range map[string]string{
		"agent:\n  limits: {daily_turns: -1}\n": "agent.limits.daily_turns",
		"telegram:\n  reply: sometimes\n":       "telegram.reply",
		"node:\n  tz: Mars/Olympus\n":           "node.tz",
		"node:\n  memory_limit: 10MB\n":         "node.memory_limit",
		"log:\n  level: loud\n":                 "log.level",
		"telegram:\n  admin: bob\n":             "telegram.admin",
		"agent:\n  search: {mode: maybe}\n":     "agent.search.mode",
	} {
		_, err := settings.Parse([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "ganglion.yaml: "+path+":") {
			t.Errorf("%q: err = %v; want path %s", src, err, path)
		}
	}
}

func TestMissingFileIsDefault(t *testing.T) {
	read := func(string) ([]byte, error) { return nil, fs.ErrNotExist }
	f, err := settings.Load("ganglion.yaml", read)
	if err != nil {
		t.Fatal(err)
	}
	if f.Node.TZ != "Europe/Kyiv" || f.Telegram.Token.Name() != "TELEGRAM_BOT_TOKEN" || f.Models != nil {
		t.Fatalf("defaults: %+v", f)
	}
}

func TestEmptyFileIsDefault(t *testing.T) {
	f, err := settings.Parse(nil)
	if err != nil || f.Agent.Limits.DailyTurns != 300 {
		t.Fatalf("got %+v, %v", f.Agent.Limits, err)
	}
}
```

- [x] **Step 2: Run the tests to see them fail**

Run: `go test ./settings/`
Expected: FAIL, the package does not exist.

- [x] **Step 3: Implement the types and `Default`**

`settings/settings.go` holds the structs listed under Interfaces, each field with a doc comment
that says what it does (these comments are the source the schema descriptions are written from),
and:

```go
// Package settings is the node's config file, ganglion.yaml (spec 7.14,
// ADR-0059): everything an operator chooses, with secrets only as env:NAME
// references. Load and Parse are strict: an unknown key or a bad value
// fails with the path in the file.
package settings

// Default is the file a node runs with when it has none: the defaults the
// flags had before M4.
func Default() File {
	return File{
		Node:    Node{TZ: "Europe/Kyiv"},
		Log:     Log{Level: "info"},
		Metrics: Metrics{Addr: "127.0.0.1:9464"},
		Telegram: Telegram{
			Token:     "env:TELEGRAM_BOT_TOKEN",
			APIURL:    "https://api.telegram.org",
			Reply:     "auto",
			Rich:      true,
			Reactions: Reactions{After: Duration(8 * time.Second), Dwell: Duration(3 * time.Second)},
		},
		Agent: Agent{
			Workspace:   "workspace",
			SessionIdle: Duration(6 * time.Hour),
			Limits:      Limits{DailyBudgetUSD: 1.0, DailyTurns: 300, DailyTokens: 1_000_000},
			Search:      Search{Mode: "auto", BraveKey: "env:BRAVE_API_KEY"},
		},
	}
}
```

The agent's defaults (300 turns, 1M tokens, 6h) are `agent.DefaultConfig()`'s today; keep one
source by reading them from there: `settings` must not import `agent`, so instead `agent`'s
`DefaultConfig` keeps its values and `settings_test.go` asserts they agree:

```go
func TestDefaultsMatchTheAgent(t *testing.T) {
	a, d := agent.DefaultConfig(), settings.Default().Agent
	if int(d.Limits.DailyTurns) != a.DailyTurns || int(d.Limits.DailyTokens) != a.DailyTokens || time.Duration(d.SessionIdle) != a.SessionIdle {
		t.Fatalf("settings defaults %+v differ from agent.DefaultConfig %+v", d, a)
	}
}
```

`settings/types.go`:

```go
package settings

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/igorrius/ganglion/internal/size"
	"github.com/igorrius/ganglion/secrets"
)

// off is the word that turns a setting off.
const off = "off"

// SecretRef is a secret by reference: "env:NAME" reads NAME or the file
// named by NAME_FILE. A literal secret is refused when the file is read.
type SecretRef string

var envName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// UnmarshalYAML refuses anything but env:NAME; the error never repeats the value.
func (r *SecretRef) UnmarshalYAML(n *yaml.Node) error {
	name, ok := strings.CutPrefix(n.Value, "env:")
	if n.Kind != yaml.ScalarNode || !ok || !envName.MatchString(name) {
		return errors.New("must be env:NAME, the variable that holds the secret; a secret is never written in the file")
	}
	*r = SecretRef(n.Value)
	return nil
}

// Name is the variable's name.
func (r SecretRef) Name() string { return strings.TrimPrefix(string(r), "env:") }

// Resolve reads the secret; "" when the variable is unset.
func (r SecretRef) Resolve(src secrets.Source) (string, error) {
	if r == "" {
		return "", nil
	}
	return src.Secret(r.Name())
}

// Duration is a Go duration or "off" (0).
type Duration time.Duration

// UnmarshalYAML reads "90s", "6h" or "off".
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Value == off {
		*d = 0
		return nil
	}
	v, err := time.ParseDuration(n.Value)
	if err != nil || v < 0 {
		return errors.New(`want a duration such as "90s" or "6h", or off`)
	}
	*d = Duration(v)
	return nil
}

// Count is a whole number of 0 or more, or "off" (0).
type Count int

// UnmarshalYAML reads a whole number or "off".
func (c *Count) UnmarshalYAML(n *yaml.Node) error {
	if n.Value == off {
		*c = 0
		return nil
	}
	v, err := strconv.Atoi(n.Value)
	if err != nil || v < 0 {
		return errors.New("must be 0 or more, or off")
	}
	*c = Count(v)
	return nil
}

// Money is US dollars, 0 or more, or "off" (0).
type Money float64

// UnmarshalYAML reads a number or "off".
func (m *Money) UnmarshalYAML(n *yaml.Node) error {
	if n.Value == off {
		*m = 0
		return nil
	}
	v, err := strconv.ParseFloat(n.Value, 64)
	if err != nil || v < 0 {
		return errors.New("must be 0 or more US dollars, or off")
	}
	*m = Money(v)
	return nil
}

// Size is a byte size in size.Parse's form; 0 is unset.
type Size uint64

// UnmarshalYAML reads "100MiB" and the like.
func (s *Size) UnmarshalYAML(n *yaml.Node) error {
	v, err := size.Parse(n.Value)
	if err != nil {
		return errors.New(`want a size such as "100MiB"`)
	}
	*s = Size(v)
	return nil
}

// UnmarshalYAML reads "off" (every limit off) or a mapping over the defaults.
func (l *Limits) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode && n.Value == off {
		*l = Limits{}
		return nil
	}
	type plain Limits
	p := plain(*l)
	if err := decodeStrict(n, &p); err != nil {
		return err
	}
	*l = Limits(p)
	return nil
}
```

`decodeStrict(n *yaml.Node, v any) error` lives in `load.go`: it re-encodes `n` and decodes it
with `KnownFields(true)`, so a custom `UnmarshalYAML` stays strict inside.

- [x] **Step 4: Implement `Parse`, `Load` and the checks with paths**

`settings/load.go`. yaml.v3 errors carry a line, not a path, so the loader walks the document
itself: it decodes the root `yaml.Node`, then each section into its struct with a `pathError`
wrapper. The pattern:

```go
package settings

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// FileName is how errors name the file, whatever its path.
const FileName = "ganglion.yaml"

// ReadFunc reads a file, as os.ReadFile does.
type ReadFunc func(path string) ([]byte, error)

// Load reads the file at path; a missing file is Default().
func Load(path string, read ReadFunc) (File, error) {
	src, err := read(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Default(), nil
	}
	if err != nil {
		return File{}, fmt.Errorf("%s: %w", FileName, err)
	}
	return Parse(src)
}

// Parse reads the file's bytes over Default() and checks every value.
func Parse(src []byte) (File, error) {
	f := Default()
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return File{}, fmt.Errorf("%s: %w", FileName, err)
	}
	if len(root.Content) == 0 {
		return f, f.check()
	}
	if err := decodeAt(root.Content[0], "", &f); err != nil {
		return File{}, err
	}
	return f, f.check()
}

// decodeAt decodes n into v strictly, naming the deepest key path an error comes from.
func decodeAt(n *yaml.Node, path string, v any) error {
	if err := decodeStrict(n, v); err != nil {
		return pathError(n, path, err)
	}
	return nil
}

func decodeStrict(n *yaml.Node, v any) error {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	if err := enc.Encode(n); err != nil {
		return err
	}
	dec := yaml.NewDecoder(&buf)
	dec.KnownFields(true)
	return dec.Decode(v)
}
```

`pathError` finds the key: for a mapping it tries each key's value node on its own into the
matching field (by yaml tag, through `reflect`), recursing until the failing leaf, and returns
`fmt.Errorf("%s: %s: %w", FileName, path, cause)`. For an unknown key it returns
`ganglion.yaml: <path>: unknown key "<key>"`. Keep it in one function under 40 lines plus a
helper that maps a tag to a field; the tests of Step 1 pin its output.

`File.check()` checks values that are not about types, each returning
`fmt.Errorf("%s: %s: %s", FileName, "<path>", "<reason>")`:

| Path | Check |
|---|---|
| `node.tz` | `time.LoadLocation` succeeds (the binary imports `time/tzdata` in `cmd`) |
| `log.level` | one of debug, info, warn, error |
| `log.dir` | any; "off" allowed |
| `metrics.addr` | "off" or `net.SplitHostPort` succeeds |
| `telegram.api_url` | `url.Parse` with scheme http or https |
| `telegram.admin` | empty or `<transport>:user:<id>` (the rule of today's `validPrincipal`, `cmd/flags/flags.go:152`, moves here) |
| `telegram.reply` | auto, always or off |
| `agent.search.mode` | auto or off |
| `agent.workspace` | not empty |

Move `validPrincipal`'s body here as `validAdmin`; `cmd/flags` drops it in task 4.

- [x] **Step 5: Run the tests**

Run: `go test ./settings/`
Expected: PASS.

- [x] **Step 6: Commit**

```bash
git add settings
git commit -m "settings: ganglion.yaml types, strict loading with paths, env:NAME secrets (R-CFG-1..3)"
```

---

### Task 3: `settings` — models and purposes; `router` by purpose (4.2; R-RTR-6)

**Files:**
- Create: `settings/models.go`, `settings/models_test.go`
- Modify: `router/router.go`, `router/router_test.go`
- Modify: `router/rules/rules.go`, `router/rules/rules_test.go`
- Delete: `router/rules/catalog.go`, `router/rules/catalog_test.go`, `router/rules/catalog_schema_test.go`

**Interfaces:**
- Produces in `router`:

```go
// Purpose is what a model call is for (R-RTR-6).
type Purpose string

const (
	PurposeChat      Purpose = "chat"      // a turn: a person's message or a scheduled agent task
	PurposeCheck     Purpose = "check"     // a short yes/no decision: the clean-context check
	PurposeCompact   Purpose = "compact"   // folding old history into a summary
	PurposeTranslate Purpose = "translate" // fixed texts into a language with no file (R-LANG-5)
	PurposeCode      Purpose = "code"      // writing modules and skills (P3)
)

// Purposes lists every purpose, in the order the file shows them.
var Purposes = []Purpose{PurposeChat, PurposeCheck, PurposeCompact, PurposeTranslate, PurposeCode}

// Effort is a model's reasoning effort; "" leaves the provider's default.
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
	EffortXHigh  Effort = "xhigh"
	EffortMax    Effort = "max"
)

type TaskReq struct {
	Purpose Purpose
	Tools   bool
	Context string // "" | "long"
	Vision  bool
	Privacy string // "" | "local-only"
}

type Model struct {
	ID       string // the catalogue id the purposes name
	Provider string
	Name     string // the provider's model name
	Caps     ModelCaps
}

// Choice is one entry of a purpose's list.
type Choice struct {
	Model  string // a Model.ID
	Effort Effort
}

type Candidate struct {
	Provider     string
	Model        string
	Effort       Effort
	PriceInMTok  float64
	PriceOutMTok float64
}
```

`Tier`, `TierCheap`, `TierSmart`, `TaskKind` and its constants, and `Model.Tier` are deleted.

- Produces in `router/rules`: `New(models []router.Model, purposes map[router.Purpose][]router.Choice, opts ...Option) (*Router, error)`.
- Produces in `settings`:

```go
type Models struct {
	Providers []Provider                   `yaml:"providers"`
	Models    []ModelDef                   `yaml:"models"`
	Purposes  map[router.Purpose][]Choice  `yaml:"purposes"`
}
type Provider struct {
	Name    string    `yaml:"name"`
	Type    string    `yaml:"type"` // openai | claude-cli
	BaseURL string    `yaml:"base_url"`
	Key     SecretRef `yaml:"key"`
	Command string    `yaml:"command"` // claude-cli: the binary, default "claude"
}
type ModelDef struct {
	ID        string  `yaml:"id"`
	Provider  string  `yaml:"provider"`
	Name      string  `yaml:"name"`
	Tools     bool    `yaml:"tools"`
	Vision    bool    `yaml:"vision"`
	Local     bool    `yaml:"local"`
	Context   int     `yaml:"context"`
	PriceIn   float64 `yaml:"price_in"`
	PriceOut  float64 `yaml:"price_out"`
	LatencyMS int     `yaml:"latency_ms"`
}
type Choice struct {
	Model  string        `yaml:"model"`
	Effort router.Effort `yaml:"effort"`
}
const (
	TypeOpenAI    = "openai"
	TypeClaudeCLI = "claude-cli"
)
func (m Models) RouterModels() []router.Model
func (m Models) RouterPurposes() map[router.Purpose][]router.Choice // defaults filled (ruling 2)
func (m Models) Keys(src secrets.Source) (map[string]string, error) // provider name → key
```

- [x] **Step 1: Write the failing tests**

`settings/models_test.go`:

```go
package settings_test

import (
	"strings"
	"testing"

	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/settings"
)

const models = `
models:
  providers:
    - {name: claude, type: claude-cli}
    - {name: or, type: openai, base_url: https://openrouter.ai/api/v1, key: env:OR_KEY}
  models:
    - {id: sonnet, provider: claude, name: sonnet, tools: true, context: 200000}
    - {id: haiku, provider: claude, name: haiku, tools: true, context: 200000}
    - {id: flash, provider: or, name: deepseek/deepseek-v4.1-flash, tools: true, context: 1048576, price_in: 0.045, price_out: 0.5}
  purposes:
    chat:  [{model: sonnet, effort: medium}, {model: haiku}]
    check: [{model: haiku, effort: low}, {model: sonnet, effort: low}]
`

func TestPurposesAndDefaults(t *testing.T) {
	f, err := settings.Parse([]byte(models))
	if err != nil {
		t.Fatal(err)
	}
	p := f.Models.RouterPurposes()
	if got := p[router.PurposeChat]; len(got) != 2 || got[0] != (router.Choice{Model: "sonnet", Effort: router.EffortMedium}) {
		t.Fatalf("chat = %+v", got)
	}
	if got := p[router.PurposeCompact]; len(got) != 2 || got[0].Model != "sonnet" {
		t.Fatalf("compact falls back to chat: %+v", got)
	}
	if _, ok := p[router.PurposeCode]; ok {
		t.Fatal("code has no default")
	}
}

func TestModelsErrors(t *testing.T) {
	for name, c := range map[string]struct{ src, want string }{
		"unknown purpose":  {strings.Replace(models, "check:", "think:", 1), `models.purposes: unknown purpose "think"`},
		"unknown model":    {strings.Replace(models, "{model: haiku}]", "{model: opus}]", 1), `models.purposes.chat[1].model: no model "opus"`},
		"bad effort":       {strings.Replace(models, "effort: medium", "effort: huge", 1), "models.purposes.chat[0].effort"},
		"effort too high":  {strings.Replace(models, "chat:  [{model: sonnet, effort: medium}", "chat:  [{model: flash, effort: max}", 1), `models.purposes.chat[0].effort: provider "or" (openai) takes low, medium or high`},
		"no chat":          {strings.Replace(models, "chat:  [{model: sonnet, effort: medium}, {model: haiku}]\n", "", 1), "models.purposes.chat: required"},
		"literal key":      {strings.Replace(models, "key: env:OR_KEY", "key: sk-123", 1), "models.providers[1].key"},
		"duplicate id":     {strings.Replace(models, "{id: haiku,", "{id: sonnet,", 1), `models.models[1].id: "sonnet" twice`},
		"openai needs key": {strings.Replace(models, ", key: env:OR_KEY", "", 1), "models.providers[1].key: required"},
	} {
		_, err := settings.Parse([]byte(c.src))
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v; want %q", name, err, c.want)
		}
	}
}

func TestIDDefaultsToName(t *testing.T) {
	f, err := settings.Parse([]byte("models:\n  providers: [{name: c, type: claude-cli}]\n  models: [{provider: c, name: haiku}]\n  purposes: {chat: [{model: haiku}]}\n"))
	if err != nil || f.Models.RouterModels()[0].ID != "haiku" {
		t.Fatalf("got %+v, %v", f.Models, err)
	}
}
```

`router/rules/rules_test.go` (replace the tier cases; keep the budget and cooling helpers the file
already has, renamed where they used tiers):

```go
func TestRouteFollowsThePurposeOrder(t *testing.T) {
	r := mustNew(t, testModels(), map[router.Purpose][]router.Choice{
		router.PurposeChat:  {{Model: "sonnet", Effort: router.EffortMedium}, {Model: "haiku"}},
		router.PurposeCheck: {{Model: "haiku", Effort: router.EffortLow}},
	})
	got := models(t, r, router.TaskReq{Purpose: router.PurposeChat})
	if want := []string{"sonnet/medium", "haiku/"}; !slices.Equal(got, want) {
		t.Fatalf("chat = %v, want %v", got, want)
	}
	got = models(t, r, router.TaskReq{Purpose: router.PurposeCheck})
	if want := []string{"haiku/low"}; !slices.Equal(got, want) {
		t.Fatalf("check = %v, want %v", got, want)
	}
}

func TestCoolingMovesToTheEnd(t *testing.T) {
	clk := newClock()
	r := mustNew(t, testModels(), chatSonnetHaiku(), rules.WithClock(clk.now))
	r.Report(router.Candidate{Provider: "p", Model: "sonnet"}, &provider.Error{Kind: provider.KindRateLimit, RetryAfter: time.Minute})
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeChat}), []string{"haiku/", "sonnet/medium"}; !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	clk.advance(2 * time.Minute)
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeChat}), []string{"sonnet/medium", "haiku/"}; !slices.Equal(got, want) {
		t.Fatalf("after cooling: got %v, want %v", got, want)
	}
}

func TestOverBudgetChatKeepsFreeModelsOnly(t *testing.T) {
	r := mustNew(t, append(testModels(), paid("flash")), map[router.Purpose][]router.Choice{
		router.PurposeChat:    {{Model: "flash"}, {Model: "haiku"}},
		router.PurposeCompact: {{Model: "flash"}},
	}, rules.WithBudget(spent(5), 1, time.UTC))
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeChat}), []string{"haiku/"}; !slices.Equal(got, want) {
		t.Fatalf("chat over budget = %v, want %v", got, want)
	}
	if got, want := models(t, r, router.TaskReq{Purpose: router.PurposeCompact}), []string{"flash/"}; !slices.Equal(got, want) {
		t.Fatalf("compact over budget = %v, want %v", got, want)
	}
}

func TestFiltersStillApply(t *testing.T) {
	r := mustNew(t, testModels(), chatSonnetHaiku())
	if _, err := r.Route(context.Background(), router.TaskReq{Purpose: router.PurposeChat, Privacy: "local-only"}); !errors.Is(err, router.ErrNoCandidate) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.Route(context.Background(), router.TaskReq{Purpose: router.PurposeCode}); !errors.Is(err, router.ErrNoCandidate) {
		t.Fatalf("code with no list: err = %v", err)
	}
}
```

with helpers `testModels()` (sonnet and haiku on provider `p`, tools, 200k, price 0),
`paid(id)` (price 0.5/0.5), `chatSonnetHaiku()`, `mustNew`, and
`models(t, r, task) []string` that returns `model/effort` per candidate.

- [x] **Step 2: Run the tests to see them fail**

Run: `go test ./settings/ ./router/...`
Expected: FAIL to compile: `router.Purpose` and the new `rules.New` do not exist.

- [x] **Step 3: Implement `router` and `router/rules`**

In `router/router.go` replace the tier and kind types with the block under Interfaces. In
`router/rules/rules.go`:

```go
// New returns a router over models with each purpose's ordered list.
// Every choice must name a model; settings checks this first, and New
// checks it again so a router is never built over a dangling id.
func New(models []router.Model, purposes map[router.Purpose][]router.Choice, opts ...Option) (*Router, error) {
	r := &Router{byID: map[string]router.Model{}, purposes: purposes, now: time.Now, loc: time.UTC,
		cooldown: DefaultCooldown, cooling: map[string]time.Time{}, models: slices.Clone(models)}
	for _, o := range opts {
		o(r)
	}
	for _, m := range models {
		r.byID[m.ID] = m
	}
	for p, list := range purposes {
		for _, c := range list {
			if _, ok := r.byID[c.Model]; !ok {
				return nil, fmt.Errorf("rules: purpose %s names no model %q", p, c.Model)
			}
		}
	}
	return r, nil
}

// Route implements router.Router: the purpose's list in order, without the
// models that do not fit the task; over budget, chat keeps free and local
// models only; cooling models move to the end.
func (r *Router) Route(ctx context.Context, t router.TaskReq) ([]router.Candidate, error) {
	over := t.Purpose == router.PurposeChat && r.overBudget(ctx)
	var out, cooling []router.Candidate
	now := r.now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.purposes[t.Purpose] {
		m := r.byID[c.Model]
		if !fits(m, t) || (over && price(m) > 0 && !m.Caps.Local) {
			continue
		}
		cand := router.Candidate{Provider: m.Provider, Model: m.Name, Effort: c.Effort,
			PriceInMTok: m.Caps.PriceInMTok, PriceOutMTok: m.Caps.PriceOutMTok}
		if r.cooling[key(m.Provider, m.Name)].After(now) {
			cooling = append(cooling, cand)
			continue
		}
		out = append(out, cand)
	}
	out = append(out, cooling...)
	if len(out) == 0 {
		return nil, router.ErrNoCandidate
	}
	return out, nil
}
```

Delete `ranked`, `taskTier`, `pick` and `rank`; `Report` stays as it is. `WithBudget`'s doc
comment becomes "over b's daily budget, chat keeps free and local models only".

- [x] **Step 4: Implement `settings/models.go`**

The types under Interfaces, decoded strictly. `Models.check(path)` is called from `File.check()`
when `Models != nil`, and checks, in order, with the paths the tests pin:

1. `providers[i].name` non-empty, unique; `type` empty means openai; type is openai or
   claude-cli; openai needs `base_url` and, unless the URL is local (`http://localhost`,
   `http://127.0.0.1`; the rule of `isLocalURL` moves here), `key`.
2. `models` non-empty; `models[i].id` defaults to `name`; ids unique (`"<id>" twice`); provider
   exists; name non-empty.
3. `purposes`: keys in `router.Purposes` (`unknown purpose "<p>"`); `chat` present and non-empty
   (`required`); each choice names a model id (`no model "<id>"`); each effort is in the closed
   set; the provider takes it: claude-cli takes all five, openai takes low, medium, high
   (`provider "<name>" (openai) takes low, medium or high`).

```go
// efforts lists the efforts each provider type takes.
var efforts = map[string][]router.Effort{
	TypeClaudeCLI: {router.EffortLow, router.EffortMedium, router.EffortHigh, router.EffortXHigh, router.EffortMax},
	TypeOpenAI:    {router.EffortLow, router.EffortMedium, router.EffortHigh},
}

// RouterPurposes returns each purpose's list; check, compact and
// translate fall back to chat's list, code has no fallback (ruling 2).
func (m Models) RouterPurposes() map[router.Purpose][]router.Choice {
	out := map[router.Purpose][]router.Choice{}
	for p, list := range m.Purposes {
		out[p] = choices(list)
	}
	for _, p := range []router.Purpose{router.PurposeCheck, router.PurposeCompact, router.PurposeTranslate} {
		if _, ok := out[p]; !ok {
			out[p] = out[router.PurposeChat]
		}
	}
	return out
}

// Keys reads each provider's key through src; a provider whose key is set
// in the file but whose variable is empty fails, naming the variable.
func (m Models) Keys(src secrets.Source) (map[string]string, error) {
	keys := map[string]string{}
	for _, p := range m.Providers {
		if p.Key == "" {
			continue
		}
		k, err := p.Key.Resolve(src)
		if err != nil {
			return nil, err
		}
		if k == "" {
			return nil, fmt.Errorf("%s: models.providers: %s (key of provider %s) is not set; set it or %s_FILE", FileName, p.Key.Name(), p.Name, p.Key.Name())
		}
		keys[p.Name] = k
	}
	return keys, nil
}
```

Delete `router/rules/catalog.go` and its tests; `RouterModels()` moves to `settings.Models`.

- [x] **Step 5: Run the tests**

Run: `go test ./settings/ ./router/...`
Expected: PASS. The rest of the repository does not compile yet; task 4 fixes the callers. Do
not commit a broken build: continue with task 4 and commit both together.

---

### Task 4: Purposes through the agent, guard, translation and skills; effort to providers (4.2)

**Files:**
- Modify: `agent/stages.go:217`, `agent/status.go:60`, `agent/compact.go:66`, `agent/model.go:36,99`,
  `agent/skills.go:26-54`, `guard/guard.go:133`, `language/translate.go:283`
- Modify: `skills/skills.go:137,243-300` and `skills/skills_test.go`
- Modify: `provider/provider.go:66-76`, `provider/openai/client.go:81-134` (+test),
  `provider/claudecli/claudecli.go:193-203` (+test)
- Modify: every test that built `router.TaskReq{Tier: …}` or `router.Model{Tier: …}` (the list in
  the code map report: agent, cmd, config, guard, internal/chatlab, router, skills tests)

**Interfaces:**
- Consumes: `router.Purpose*`, `router.Effort*`, `router.Candidate.Effort` (task 3).
- Produces: `provider.ChatRequest.Effort router.Effort`; skills frontmatter
  `model: {purpose, tools, context, vision, privacy}`.

- [x] **Step 1: Write the failing tests**

`provider/openai/client_test.go`:

```go
func TestEffortIsSentAsReasoningEffort(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	c := openai.New("t", srv.URL, "k")
	if _, err := c.Chat(context.Background(), provider.ChatRequest{Model: "m", Effort: router.EffortLow,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}}); err != nil {
		t.Fatal(err)
	}
	if body["reasoning_effort"] != "low" {
		t.Fatalf("body = %v", body)
	}
}

func TestNoEffortSendsNoField(t *testing.T) {
	// as above with Effort "": the body has no "reasoning_effort" key.
}
```

(Write the second test in full in the file, the same as the first with `Effort` unset and the
assertion `if _, ok := body["reasoning_effort"]; ok { t.Fatalf(...) }`.)

`provider/claudecli/claudecli_test.go`: the test that captures the CLI's arguments through the
fake command (the file's existing helper that records `os.Args` of the fake binary) gets a case
with `Effort: router.EffortHigh` asserting the args contain `"--effort", "high"`, and a case
without effort asserting they contain no `--effort`.

`skills/skills_test.go`: a skill with `model: {purpose: check}` loads with
`Skill.Model.Purpose == router.PurposeCheck`; `model: {tier: cheap}` fails naming `tier`;
`model: {purpose: think}` fails naming the purpose.

`guard/guard_test.go`: the fake router records the `TaskReq`; assert `Purpose == router.PurposeCheck`.
`agent` tests: the recorded task of a message turn has `Purpose == router.PurposeChat`, of
compaction `router.PurposeCompact`. `language` tests: translation routes `router.PurposeTranslate`.

- [x] **Step 2: Run them to see them fail**

Run: `go test ./provider/... ./skills/ ./guard/ ./agent/ ./language/`
Expected: compile errors on `Effort` and `Purpose`.

- [x] **Step 3: Implement**

- `provider.ChatRequest` gains `Effort router.Effort // "" = the provider's default`.
- openai `wireRequest` gains `ReasoningEffort string \`json:"reasoning_effort,omitempty"\``, set
  in `toWire` from `string(req.Effort)`.
- claudecli `Chat`: after the `--model` pair,

```go
if req.Effort != "" {
	args = append(args, "--effort", string(req.Effort))
}
```

- `agent/model.go`: where the loop sets `req.Model = c.Model` (:50), also set
  `req.Effort = c.Effort`; the log key `kind` and the metric attribute `task` take
  `string(task.Purpose)` (ruling 6).
- The callers: `stages.go:217` and `status.go:60` → `Purpose: router.PurposeChat`;
  `compact.go:66` → `router.TaskReq{Purpose: router.PurposeCompact, Privacy: st.privacy()}`;
  `guard.go:133` → `router.TaskReq{Purpose: router.PurposeCheck}`, and its own loop sets
  `req.Effort = c.Effort` where it sets the model; `translate.go:283` →
  `router.TaskReq{Purpose: router.PurposeTranslate}` and passes `cands[0].Effort`.
- `agent/skills.go` `skillRoute` copies `sk.Model.Purpose` (empty keeps the turn's purpose).
- `skills/skills.go`: `frontmatter.Model.Tier` becomes
  `Purpose router.Purpose \`yaml:"purpose"\``; `checkModel` checks the purpose is in
  `router.Purposes` (empty allowed) and keeps the context and privacy checks.
- Every test fixture with tiers: a `router.Model` gets an `ID` instead of a `Tier`; a router is
  built with `rules.New(models, map[router.Purpose][]router.Choice{router.PurposeChat: …})`.

- [x] **Step 4: Run the tests**

Run: `go test ./provider/... ./skills/ ./guard/ ./agent/ ./language/ ./router/... ./settings/`
Expected: PASS. `cmd`, `config` and `internal/chatlab` still use the old catalogue; task 5
moves them. Continue with task 5 before committing, so no commit breaks the build.

---

### Task 5: The node reads `ganglion.yaml`; retired variables fail the start (4.1, 4.2; R-CFG-1, 5, 7)

**Files:**
- Create: `settings/retired.go`, `settings/retired_test.go`
- Modify: `cmd/flags/flags.go` (most of `Run.Flags` goes), `cmd/run.go`, `cmd/mcp.go`, `cmd/cmd_test.go`,
  `cmd/run_test.go`, `cmd/crash_test.go`, `cmd/diag_fault_test.go`, `cmd/memory_test.go`
- Modify: `config/bootstrap.go`, `config/agent.go:276-317`, `config/config.go:106`
- Modify: `internal/chatlab/node.go:146-149,286-291`, `internal/chatlab/program.go:71-111`
- Modify: `config/lab.go:30-41,121-137`, `config/accept_test.go:113-236`, `config/*_test.go` with catalogues

**Interfaces:**
- Consumes: `settings.Load`, `settings.File`, `settings.Models.{RouterModels,RouterPurposes,Keys}`,
  `memlimit.Decide/Apply`.
- Produces:
  - `settings.Retired(getenv func(string) string) error` — the first retired variable set, as
    `GANGLION_TZ is no longer read: set node.tz in ganglion.yaml`;
  - `config.AgentSettings.Models *settings.Models` replaces `Catalog *rules.Catalog`;
  - `flags.Run{Config, DB, Roles string}`; `GANGLION_CONFIG` is the `--config` flag's variable,
    default `ganglion.yaml` beside the database.

- [x] **Step 1: Write the failing tests**

`settings/retired_test.go`:

```go
func TestRetiredVariablesFail(t *testing.T) {
	env := map[string]string{"GANGLION_TZ": "UTC"}
	err := settings.Retired(func(k string) string { return env[k] })
	if err == nil || err.Error() != "GANGLION_TZ is no longer read: set node.tz in ganglion.yaml" {
		t.Fatalf("err = %v", err)
	}
}

func TestKeptVariablesPass(t *testing.T) {
	env := map[string]string{"GANGLION_CONFIG": "x", "GANGLION_DB": "x", "GANGLION_MCP_TOKEN": "x",
		"GANGLION_LIVE_ACCEPT": "1", "GANGLION_ACCEPT_OUT": "x", "GANGLION_MEASURE": "1", "GANGLION_CRASH_AGENT": "1",
		"OTEL_EXPORTER_OTLP_ENDPOINT": "http://x", "TELEGRAM_BOT_TOKEN": "t"}
	if err := settings.Retired(func(k string) string { return env[k] }); err != nil {
		t.Fatal(err)
	}
}

func TestEveryOldFlagVariableIsRetired(t *testing.T) {
	// The variables the run flags read before M4 (cmd/flags/flags.go at 4e39ef3).
	old := []string{"GANGLION_LOG_LEVEL", "GANGLION_ADMIN", "TELEGRAM_API_URL", "GANGLION_TELEGRAM_REPLY",
		"GANGLION_TELEGRAM_RICH", "GANGLION_REACTION_AFTER", "GANGLION_REACTION_DWELL", "GANGLION_METRICS_ADDR",
		"GANGLION_LOG_DIR", "GANGLION_MODELS_FILE", "GANGLION_LANGUAGES", "GANGLION_WORKSPACE",
		"GANGLION_SANDBOX_DENY", "GANGLION_SANDBOX_HIDE_NAMES", "GANGLION_DAILY_BUDGET_USD",
		"GANGLION_DAILY_TURNS", "GANGLION_DAILY_TOKENS", "GANGLION_SESSION_IDLE", "GANGLION_TZ", "GANGLION_SEARCH"}
	for _, k := range old {
		if err := settings.Retired(func(v string) string { if v == k { return "x" }; return "" }); err == nil {
			t.Errorf("%s is not retired", k)
		}
	}
}
```

`cmd/run_test.go` (replace the env-based cases):

```go
func TestBootstrapFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "ganglion.yaml")
	mustWrite(t, path, "node: {tz: UTC}\nagent: {limits: {daily_turns: 7}}\ntelegram: {token: env:BOT}\n")
	env := map[string]string{"BOT": "t0k"}
	b, err := bootstrap(flags.Run{Config: path, DB: filepath.Join(dir, "g.db")}, fakeEnv(env), fakeEnv(env).get, os.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	if b.Agent.TZ.String() != "UTC" || b.Agent.DailyTurns != 7 || b.BotToken != "t0k" {
		t.Fatalf("got %+v", b)
	}
}

func TestRetiredVariableFailsTheStart(t *testing.T) {
	env := fakeEnv{"GANGLION_TZ": "UTC"}
	_, err := bootstrap(flags.Run{Config: "none.yaml", DB: "g.db"}, env, env.get, os.ReadFile)
	if err == nil || !strings.Contains(err.Error(), "set node.tz in ganglion.yaml") {
		t.Fatalf("err = %v", err)
	}
}

func TestMemoryLimitInBootstrap(t *testing.T) {
	// node.memory_limit: 100MiB → b.MemoryLimit == 100<<20 (Source config).
}
```

(`fakeEnv map[string]string` implements `secrets.Source` and has `get(k string) string`; write
it in the test file.)
`cmd/cmd_test.go:173`'s help assertion now expects `--config`, `GANGLION_CONFIG`, `--db`,
`--roles` and no `--tz` or `--models-file`.

- [x] **Step 2: Run them to see them fail**

Run: `go test ./settings/ ./cmd/`
Expected: FAIL.

- [x] **Step 3: Implement**

`settings/retired.go`:

```go
package settings

import "fmt"

// retired maps each variable that held a setting before M4 to the key that
// replaced it (R-CFG-5). Variables not listed here are not ganglion's
// settings: the config path, the database path, secrets, OpenTelemetry's
// own, the bridge's per-run token and the test tools' variables.
var retired = []struct{ env, key string }{
	{"GANGLION_LOG_LEVEL", "log.level"},
	{"GANGLION_ADMIN", "telegram.admin"},
	{"TELEGRAM_API_URL", "telegram.api_url"},
	{"GANGLION_TELEGRAM_REPLY", "telegram.reply"},
	{"GANGLION_TELEGRAM_RICH", "telegram.rich"},
	{"GANGLION_REACTION_AFTER", "telegram.reactions.after"},
	{"GANGLION_REACTION_DWELL", "telegram.reactions.dwell"},
	{"GANGLION_METRICS_ADDR", "metrics.addr"},
	{"GANGLION_LOG_DIR", "log.dir"},
	{"GANGLION_MODELS_FILE", "models"},
	{"GANGLION_LANGUAGES", "agent.languages"},
	{"GANGLION_WORKSPACE", "agent.workspace"},
	{"GANGLION_SANDBOX_DENY", "sandbox.deny"},
	{"GANGLION_SANDBOX_HIDE_NAMES", "sandbox.hide_names"},
	{"GANGLION_DAILY_BUDGET_USD", "agent.limits.daily_budget_usd"},
	{"GANGLION_DAILY_TURNS", "agent.limits.daily_turns"},
	{"GANGLION_DAILY_TOKENS", "agent.limits.daily_tokens"},
	{"GANGLION_SESSION_IDLE", "agent.session_idle"},
	{"GANGLION_TZ", "node.tz"},
	{"GANGLION_SEARCH", "agent.search.mode"},
}

// Retired returns an error for the first retired variable that is set.
func Retired(getenv func(string) string) error {
	for _, r := range retired {
		if getenv(r.env) != "" {
			return fmt.Errorf("%s is no longer read: set %s in %s", r.env, r.key, FileName)
		}
	}
	return nil
}
```

`cmd/flags/flags.go`: `Run` becomes `{Config, DB, Roles string}`; `Run.Flags()` (no
arguments) returns `--config` (`GANGLION_CONFIG`, `TakesFile`, empty default meaning
"ganglion.yaml beside the database"), `DBFlag` and `--roles`. `Root`'s `--log-level` goes (the
level comes from the file; `newLogger` takes it from `settings.File.Log.Level`). `Idle`,
`validPrincipal`, `validLevel`, `oneOf`, `nonNegative`, `in` go. `LogDirFlag` stays for
`ganglion mcp`, which has no file of its own to read when only the logs are wanted; `mcp` also
gets `--config` so `config.show` can show the file (Step 4).

`cmd/run.go` `bootstrap`:

```go
// bootstrap reads the config file and the secrets it references into
// config.Bootstrap. A retired variable fails first: a stale deploy must
// not run on defaults (R-CFG-5).
func bootstrap(v flags.Run, src secrets.Source, getenv func(string) string, read func(string) ([]byte, error)) (config.Bootstrap, error) {
	if err := settings.Retired(getenv); err != nil {
		return config.Bootstrap{}, err
	}
	path := cmp.Or(v.Config, filepath.Join(filepath.Dir(v.DB), settings.FileName))
	f, err := settings.Load(path, read)
	if err != nil {
		return config.Bootstrap{}, err
	}
	return fromFile(f, v.DB, src)
}
```

`runCommand` calls it as `bootstrap(v, secrets.OSEnv(), os.Getenv, os.ReadFile)`. `fromFile(f, db, src)`
maps every field the old `bootstrap` set from flags (the table in the code map report, section A)
from `f` instead: `DB`, `Languages` (default beside the database as today), `BotToken`
(`f.Telegram.Token.Resolve`), `BotAPIURL`, `Admin`, `TelegramReply`, `TelegramRich`,
`ReactionAfter/Dwell`, `MetricsAddr`, `LogDir` (the old `flags.LogDir(db, v)` rule moves to
`settings.File.LogDir(db string) (string, bool)`), `SandboxDeny`, `SandboxHideNames`,
`ModulesDir`, `ClaudeDir`, `Agent.Workspace`, `Agent.DailyBudgetUSD`, `Agent.DailyTurns`,
`Agent.DailyTokens`, `Agent.SessionIdle`, `Agent.TZ` (`time.LoadLocation`), `Agent.BraveKey`,
`Agent.SearchOff`, and `Agent.Models = f.Models` with `Agent.Keys` from `f.Models.Keys(src)`. It
also sets the new `Bootstrap.MemoryLimit memlimit.Limit` from
`memlimit.Decide(uint64(f.Node.MemoryLimit), os.ReadFile)`, and the new `Bootstrap.Settings settings.File`
(for `config.show` and M4b). `runCommand`'s Action calls `memlimit.Apply(b.MemoryLimit)` right
after `bootstrap` and logs `"memory limit" bytes=… source=…` in `announce`.

`config/agent.go:276-280`:

```go
r, err := rules.New(b.Agent.Models.RouterModels(), b.Agent.Models.RouterPurposes(),
	rules.WithBudget(tl, b.Agent.DailyBudgetUSD, b.Agent.TZ))
```

`providerFactories` iterate `b.Agent.Models.Providers` (type `settings.Provider`); the skills deny
list (:200-206) adds each `p.Key.Name()` instead of `p.KeyEnv`. `agentOn(b)` becomes
`b.Agent.Models != nil`. `config/lab.go` `LabOptions.Catalog rules.Catalog` becomes
`Models settings.Models`. `internal/chatlab`:
- `node.go:146-149` builds `AgentSettings{Models: &m, Keys: keys, …}`;
- the scripted catalogue (:286-291) becomes a `settings.Models` with ids `scripted` and `guard`
  and purposes `chat: [scripted]`, `check: [guard]`;
- `program.go` `MainModel` takes the first entry of `chat`; `KeysFor` reads `p.Key.Name()` and
  refuses a `TELEGRAM_*` name as today.

`announce` drops "no GANGLION_MODELS_FILE" for "no models section in ganglion.yaml; the agent is
off", and "no TELEGRAM_BOT_TOKEN" names `f.Telegram.Token.Name()`.

- [x] **Step 4: `ganglion mcp`'s `config.show`**

`cmd/mcp.go` `runSettings` rebuilt: it takes the loaded `settings.File` and lists each section's
keys and values as `diag.Settings` rows, secrets as "`env:NAME` (set)" or "(unset)", never the
value; models as `id provider/name` and purposes as `chat: sonnet (medium), haiku`. Its test
asserts no secret value appears and the purposes line is there.

- [x] **Step 5: Run every test**

Run: `make lint test`
Expected: PASS. Fix every remaining compile error in tests from the list of task 4 (they now
build `settings.Models` or `router.Model{ID: …}`).

- [x] **Step 6: Docs and commit (tasks 3–5 together)**

`docs/architecture/05-code-map.md`: add `settings` ("ganglion.yaml: types, strict loading,
`env:NAME`, retired variables, models by purpose") with its dependency rule (`settings` imports
`router`, `secrets`, `internal/size`; nothing imports `settings` but `cmd`, `config`,
`internal/chatlab` and tools); `router` loses tiers; `router/rules` loses the catalogue.
`docs/architecture/03-components.md`: the configuration arrow comes from the file.

```bash
git add -A settings router provider agent guard language skills cmd config internal/chatlab docs/architecture
git commit -m "settings: the node reads ganglion.yaml; models by purpose with effort; retired variables fail the start (R-CFG-1,5,7, R-RTR-6)"
```

---

### Task 6: The schema `schemas/node.schema.json` and the example files (4.1; R-CFG-4)

**Files:**
- Create: `schemas/node.schema.json`, `settings/schema_test.go`
- Modify: `schemas/schemas_test.go` (the list of schemas; `TestAt` moves to the node schema)
- Delete: `schemas/models.schema.json`
- Create: `examples/ganglion.example.yaml`, `examples/ganglion.lab.yaml`
- Delete: `models.example.yaml`, `tools/chatlab/haiku-only.yaml` (→ `examples/ganglion.haiku-only.yaml`)
- Modify: `.gitignore` (`models.local.yaml` → `ganglion.local.yaml`)

**Interfaces:**
- Consumes: `schemas.At`, `schemas.YAMLFields`, the `settings` types.

- [x] **Step 1: Write the failing test**

```go
// settings/schema_test.go
package settings_test

import (
	"os"
	"slices"
	"testing"

	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/schemas"
	"github.com/igorrius/ganglion/settings"
)

func TestSchemaMatchesTheLoader(t *testing.T) {
	schema, err := schemas.Get("node.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	for pointer, v := range map[string]any{
		"":                       settings.File{},
		"/$defs/node":            settings.Node{},
		"/$defs/log":             settings.Log{},
		"/$defs/metrics":         settings.Metrics{},
		"/$defs/telegram":        settings.Telegram{},
		"/$defs/reactions":       settings.Reactions{},
		"/$defs/agent":           settings.Agent{},
		"/$defs/limits":          settings.Limits{},
		"/$defs/search":          settings.Search{},
		"/$defs/sandbox":         settings.Sandbox{},
		"/$defs/models":          settings.Models{},
		"/$defs/provider":        settings.Provider{},
		"/$defs/model":           settings.ModelDef{},
		"/$defs/choice":          settings.Choice{},
		"/$defs/module":          settings.Module{},
	} {
		o, err := schemas.At(schema, pointer)
		if err != nil {
			t.Fatalf("%q: %v", pointer, err)
		}
		if want := schemas.YAMLFields(v); !slices.Equal(o.Properties, want) {
			t.Errorf("%q: schema properties %v, loader fields %v", pointer, o.Properties, want)
		}
		if o.AdditionalProperties {
			t.Errorf("%q: additionalProperties must be false: the loader is strict", pointer)
		}
	}
	purposes, _ := schemas.At(schema, "/$defs/purposes")
	want := make([]string, len(router.Purposes))
	for i, p := range router.Purposes {
		want[i] = string(p)
	}
	slices.Sort(want)
	if !slices.Equal(purposes.Properties, want) {
		t.Errorf("purposes %v, router.Purposes %v", purposes.Properties, want)
	}
}

func TestEveryPropertyIsDescribed(t *testing.T) {
	// Walk the schema JSON: every object under "properties" has a non-empty "description".
}

func TestExamplesLoad(t *testing.T) {
	for _, p := range []string{"../examples/ganglion.example.yaml", "../examples/ganglion.lab.yaml", "../examples/ganglion.haiku-only.yaml"} {
		src, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := settings.Parse(src); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
}
```

Write `TestEveryPropertyIsDescribed` in full: unmarshal the schema into `map[string]any`,
recurse through every `"properties"` map and every `"$defs"` entry, and fail with the JSON
pointer of each property whose `description` is missing or empty. The enums of `telegram.reply`,
`log.level`, `agent.search.mode`, `provider.type` and `choice.effort` are asserted against the
loader's sets the same way `catalog_schema_test.go` did.

- [x] **Step 2: Run it to see it fail**

Run: `go test ./settings/ -run Schema`
Expected: FAIL: no `node.schema.json`.

- [x] **Step 3: Write the schema**

`schemas/node.schema.json`, draft 2020-12, `$id`
`https://raw.githubusercontent.com/igorrius/ganglion/master/schemas/node.schema.json`, a top
`description` ("ganglion.yaml: everything an operator chooses (spec 7.14, ADR-0059). Secrets
appear only as env:NAME."). Every property carries `description`, `type` and, where the loader
has one, `default` and `enum`. Shared definitions: `secretRef` (`pattern`
`^env:[A-Z_][A-Z0-9_]*$`), `duration` (`pattern` of Go durations or `off`), `count`
(non-negative integer or `off`), `size` (`pattern` `^[0-9]+(KiB|MiB|GiB)?$`). `limits` is
`oneOf: [{const: off}, {type: object, …}]`. `purposes` is an object whose properties are the
five purposes, each an array of `choice`. Descriptions are the field doc comments of task 2,
and say what "off" and a missing key mean, e.g. for `node.memory_limit`: "The node's soft memory
limit (GOMEMLIMIT). Without it the node takes about 80 % of its container's limit, or none.
Set it when the container is sized for module builds."

- [x] **Step 4: Write the example files**

`examples/ganglion.example.yaml`: every section with a comment on every field, the models of the
design (claude sonnet, haiku, opus; the openrouter provider and flash commented out) and the
purposes `chat: [sonnet medium, haiku]`, `check: [haiku low, sonnet low]`, `compact: [haiku]`,
`code: [opus high, sonnet high]`; `modules: []` with a comment that `module.install` writes it
(M4b). First line: `# yaml-language-server: $schema=../schemas/node.schema.json`.

`examples/ganglion.lab.yaml`: what `make accept`, `make live-agent` and chatlab need: the
claude provider, sonnet and haiku, the same purposes without `code`, `metrics: {addr: off}`,
`log: {dir: off}`.

`examples/ganglion.haiku-only.yaml`: the content of `tools/chatlab/haiku-only.yaml` in the new form
(`chat: [haiku]`).

- [x] **Step 5: Run the tests and commit**

Run: `make lint test`
Expected: PASS.

```bash
git add schemas settings/schema_test.go examples .gitignore
git rm models.example.yaml tools/chatlab/haiku-only.yaml schemas/models.schema.json
git commit -m "schemas: node.schema.json describes every field of ganglion.yaml; example files (R-CFG-4)"
```

---

### Task 7: The tools and scripts on config files

**Files:**
- Modify: `Makefile:19-101`, `.env.example`, `README.md:30-46`, `docs/README.md:294,322,479,504-554`,
  `examples/workspace/persona/10-identity.md:3`
- Modify: `tools/chatlab/main.go:49,151-248,274`, `tools/chatlab/passed.json` (regenerated by a lab run),
  `tools/modelcompare/main.go:5,145-187`
- Modify: `config/accept_test.go:113-236`, `cmd/crash_test.go:267`, `cmd/diag_fault_test.go:43,65`,
  `cmd/memory_test.go:41-156`, `config/measure_test.go`

**Interfaces:**
- Consumes: `settings.Parse`, `settings.Models`.

- [x] **Step 1: Tests and harnesses write a file instead of variables**

A helper in each test package that needs one (`cmd`, `config`):

```go
// writeConfig writes ganglion.yaml into dir and returns its path.
func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "ganglion.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}
```

- `cmd/memory_test.go`: `GANGLION_MODELS_FILE` + `FAKE_KEY` + `GANGLION_DAILY_TURNS=0` become a
  file with a `models:` section (`key: env:FAKE_KEY`) and `agent: {limits: {daily_turns: off}}`,
  passed with `--config`.
- `cmd/crash_test.go`, `cmd/diag_fault_test.go`: the same pattern for their models.
- `config/accept_test.go`: `cliCatalog`/`apiCatalog` stop rewriting `models.example.yaml` by line
  prefixes; they build `settings.Models` values in Go (claude sonnet+haiku; openrouter flash with
  `key: env:OPENROUTER_API_KEY`) and set them on the `Bootstrap`.
- `config/measure_test.go`: keeps `GANGLION_MEASURE*` (test variables, not settings).

- [x] **Step 2: Makefile and env example**

```make
# .env holds TELEGRAM_TEST_BOT_TOKEN and the provider keys; settings live in LIVE_CONFIG.
LIVE_CONFIG ?= ganglion.local.yaml

live-echo:
	set -a; . ./.env; set +a; TELEGRAM_BOT_TOKEN=$$TELEGRAM_TEST_BOT_TOKEN TELEGRAM_BOT_TOKEN_FILE= \
	  ./bin/ganglion run --roles core --db $(LIVE_DB) --config examples/ganglion.echo.yaml

live-agent: build
	@test -f $(LIVE_CONFIG) || { echo "copy examples/ganglion.example.yaml to $(LIVE_CONFIG) and set telegram.admin"; exit 1; }
	set -a; . ./.env; set +a; TELEGRAM_BOT_TOKEN=$$TELEGRAM_TEST_BOT_TOKEN TELEGRAM_BOT_TOKEN_FILE= \
	  ./bin/ganglion run --roles core --db $(LIVE_DB) --config $(LIVE_CONFIG)
```

Keep each target's existing prerequisites and lines other than the variables; `compare` passes
`-config $(LIVE_CONFIG)`. `examples/ganglion.echo.yaml` has no `models:` section and a commented
`telegram.admin` line to fill in; add it to `TestExamplesLoad`. `.env.example` drops `GANGLION_ADMIN` and
`GANGLION_TZ` and says settings live in `ganglion.local.yaml`.

- [x] **Step 3: chatlab and modelcompare**

- `tools/chatlab/main.go`: `-models` becomes `-config`, default `examples/ganglion.lab.yaml`;
  `loadCatalog` becomes `loadModels(path) (settings.Models, map[string]string, error)` through
  `settings.Load` (a file without `models:` is an error naming the file); `ScrubEnvironment`
  stays; `passed.json`'s hash now covers `examples/ganglion.lab.yaml`.
- `tools/modelcompare/main.go`: `-models` becomes `-config`; it reads `settings.Load(...).Models`
  and keys through `Models.Keys(secrets.OSEnv())`.

- [x] **Step 4: Docs**

`README.md` run instructions: copy `examples/ganglion.example.yaml` to `ganglion.yaml` beside the
database, set `telegram.admin`, put `TELEGRAM_BOT_TOKEN` and provider keys in the environment,
run `./bin/ganglion run`. `docs/README.md` owner setup and the sandbox notes: the old variables
become their keys (`telegram.admin`, `agent.languages`, `sandbox.deny`, `sandbox.hide_names`).
`examples/workspace/persona/10-identity.md:3`: `agent.workspace`.

- [x] **Step 5: Run everything**

Run: `make lint test && make lab` (the scripted lab scenarios; `make lab` needs no network)
Expected: PASS; `passed.json` regenerated by the lab run.

- [x] **Step 6: Commit**

```bash
git add -A Makefile .env.example README.md docs examples tools cmd config
git commit -m "tools, tests and docs read ganglion.yaml; the live targets take LIVE_CONFIG"
```

---

### Task 8: `/restart` (R-CFG-6)

**Files:**
- Create: `node/restart.go`, `node/restart_test.go`
- Create: `agent/restart.go`, `agent/restart_test.go`
- Modify: `agent/commands.go:41-76` (the table), the agent's `Deps` struct (`agent/agent.go`)
- Modify: `config/agent.go:70-105` (provides `Restart`), `config/config.go:63-73`, `main.go:19-24`
- Modify: `texts` (the reply keys, in the three languages the fixed texts have)

**Interfaces:**
- Produces:
  - `node.ErrRestart` (sentinel); `type RestartFunc func()` in `agent`;
  - `config.Run` returns `node.ErrRestart` when the node stopped for `/restart`;
  - `main` exits 75 on `node.ErrRestart`.

- [x] **Step 1: Write the failing tests**

```go
// agent/restart_test.go
func TestRestartIsAdminOnly(t *testing.T) {
	var asked int
	a := newTestAgent(t, withRestart(func() { asked++ }))
	reply := a.handle(t, msgFrom(nonAdmin, "/restart"))
	if asked != 0 || !strings.Contains(reply, textOf(texts.ReplyNotAvailable)) {
		t.Fatalf("asked %d, reply %q", asked, reply)
	}
	reply = a.handle(t, msgFrom(admin, "/restart"))
	if asked != 1 || !strings.Contains(reply, textOf(texts.ReplyRestarting)) {
		t.Fatalf("asked %d, reply %q", asked, reply)
	}
}
```

Use the agent test package's existing helpers for building an agent and sending a message as an
admin or not (the `/status` and `/modules` tests show them; reuse their names).

```go
// node/restart_test.go
func TestRunReturnsErrRestart(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(node.ErrRestart)
	err := node.Run(ctx, nil, func() ([]node.Component, error) { return nil, nil }, slog.New(slog.DiscardHandler))
	if !errors.Is(err, node.ErrRestart) {
		t.Fatalf("err = %v", err)
	}
}
```

- [x] **Step 2: Run them to see them fail**

Run: `go test ./agent/ -run Restart ./node/ -run Restart`
Expected: FAIL.

- [x] **Step 3: Implement**

- `node/restart.go`: `var ErrRestart = errors.New("node: restart requested")`. In `node.Run`, where
  it returns nil on `ctx.Err() != nil`, return `context.Cause(ctx)` when it is `ErrRestart`.
- `config.Run` creates `ctx, cancel := context.WithCancelCause(ctx)` and provides
  `agent.RestartFunc(func() { cancel(node.ErrRestart) })` to the agent's deps; it returns the
  error `node.Run` returned after the deferred shutdown.
- `agent/restart.go`:

```go
// restart ends the node so its supervisor starts it again (R-CFG-6): the
// settings outside modules apply at start. Admin only.
func (a *Agent) restart(ctx context.Context, t *turn, _ string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	t.reply(a.say(ctx, t, texts.ReplyRestarting, nil))
	a.d.Restart()
	return nil
}
```

  Table entry after `/status`: `{word: "/restart", run: a.restart, hint: hints("restart the node:
  settings apply", …)}` in the three languages the other hints use. `texts.ReplyRestarting`:
  "Restarting. Back in a few seconds." and its translations. The reply goes through the outbox
  before the cancel takes effect; delivery after restart is guaranteed (M2), so a reply sent
  after the stop is still delivered.
- `main.go`:

```go
	if err := cmd.Execute(ctx, os.Args, os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, node.ErrRestart) {
			os.Exit(exitRestart)
		}
		fmt.Fprintln(os.Stderr, "ganglion:", err)
		os.Exit(1)
	}
```

  with `const exitRestart = 75 // EX_TEMPFAIL: a supervisor that restarts on failure starts it again`.

- [x] **Step 4: Run the tests**

Run: `make lint test`
Expected: PASS.

- [x] **Step 5: Commit**

```bash
git add node agent config main.go texts
git commit -m "/restart ends the node with code 75 for its supervisor to start again (R-CFG-6)"
```

---

### Task 9: Close M4a — docs, memory, the live check

**Files:**
- Modify: `docs/plans/p1-all-in-one.md` (4.1 and 4.2 marked done), `docs/README.md` ("Where we
  are"), `docs/architecture/05-code-map.md`, this plan's checkboxes
- The board: G-117 and G-122 to done

- [ ] **Step 1: Measure**

Run: `make memory`
Expected: idle RSS and after-1000-turns RSS within 2 MB of M3d's numbers in `docs/README.md`; a
growth needs a reason in the README.

- [ ] **Step 2: Check GOMEMLIMIT in a container**

Run: `docker run --rm -m 256m -v "$PWD/bin:/b" debian:stable-slim /b/ganglion run --roles core --db /tmp/g.db`
(stop it after the "memory limit" log line).
Expected: `memory limit bytes=214748364 source=cgroup`.

- [ ] **Step 3: The bad-config checks of the design's acceptance (check 1)**

By hand, each must fail the start with a message naming the path:
- an unknown key (`agent: {limitz: {}}`);
- a literal secret (`telegram: {token: 123:ABC}`): the message has no `123:ABC`;
- `GANGLION_TZ=UTC` set.

- [ ] **Step 4: Live**

`make live-agent` with `ganglion.local.yaml` from the example: a turn answers; `/status` shows
the models in the `chat` order; `/restart` replies and the node comes back (run it under a loop
or a supervisor that restarts on exit 75); the clean-context check runs on Haiku (the turn trace
shows `check` with `haiku`).

- [ ] **Step 5: Docs and commit**

Tick 4.1 and 4.2 in the phase plan; "Where we are" gains an M4a line (one file, models by
purpose, `/restart`, measured numbers).

```bash
git add docs
git commit -m "docs: M4a closed — one config file and models by purpose"
```
