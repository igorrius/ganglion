package modstore_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/modstore"
)

const localManifest = `name: x
version: 1
abi: 1
triggers:
  - cron: "*/30 * * * *"
  - tool: soc
    about: the battery charge
    output:
      soc: number
capabilities:
  http.fetch: [192.168.1.5]
  secret.get: [INVERTER_KEY]
  state: true
cells:
  report:
    soc:
      kind: sensor
      type: number
      about: battery charge
    grid:
      kind: sensor
      type: number
      about: grid power
  read:
    - house.load
limits:
  memory: 64MiB
  timeout: 20s
`

func noOwners(context.Context, string) (string, bool, error) { return "", false, nil }
func allSet(string) bool                                     { return true }

var hostConfig = map[string]any{"host": "192.168.1.5"}

func withKey(manifest string) string {
	return manifest + "config:\n  host:\n    type: string\n    required: true\n"
}

func build(name string, wasm []byte, manifest string) modstore.Build {
	return modstore.Build{Module: name, Wasm: wasm, Manifest: []byte(manifest), Language: "rust", By: "telegram:1", Note: "first", Commit: "abc123"}
}

func plan(t *testing.T, s *modstore.Store, b modstore.Build, owners modstore.CellOwners) modstore.Plan {
	t.Helper()
	p, err := s.PlanLocal(context.Background(), b, allSet, hostConfig, owners)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func install(t *testing.T, s *modstore.Store, b modstore.Build) modstore.Outcome {
	t.Helper()
	p := plan(t, s, b, noOwners)
	if p.Refusal != "" {
		t.Fatalf("refused: %s", p.Refusal)
	}
	o, err := s.InstallLocal(context.Background(), b, p.SHA256)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestPlanOfAFirstInstallListsTheRights(t *testing.T) {
	s := newEnv(t).open(t)
	b := build("local/x", variant("a"), withKey(localManifest))
	p := plan(t, s, b, noOwners)
	if p.Refusal != "" || p.SHA256 != modstore.Digest(b.Wasm, b.Manifest) || p.Version != 1 || p.Size != len(b.Wasm) {
		t.Fatalf("plan %+v", p)
	}
	r := p.Rights
	if !slices.Equal(r.Hosts, []string{"192.168.1.5"}) || !slices.Equal(r.Secrets, []string{"INVERTER_KEY"}) ||
		!slices.Equal(r.CellsCreated, []string{"x.grid", "x.soc"}) || len(r.CellsTaken) != 0 ||
		!slices.Equal(r.CellsRead, []string{"house.load"}) || !slices.Equal(r.Crons, []string{"*/30 * * * *"}) ||
		!slices.Equal(r.Tools, []string{"soc"}) || r.Memory != 64<<20 || r.Timeout != 20*time.Second {
		t.Fatalf("rights %+v", r)
	}
	if w := p.Widening; len(w.Hosts)+len(w.Secrets)+len(w.CellsCreated)+len(w.CellsRead)+len(w.Crons)+len(w.Tools) != 0 || w.Memory != 0 || w.Timeout != 0 {
		t.Fatalf("a first install widens nothing: %+v", w)
	}
	if v := mustList(t, s); len(v) != 0 {
		t.Fatalf("planning stored something: %+v", v)
	}
}

func mustList(t *testing.T, s *modstore.Store) []modstore.Version {
	t.Helper()
	vs, err := s.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var out []modstore.Version
	for _, v := range vs {
		if v.Module != "html2md" {
			out = append(out, v)
		}
	}
	return out
}

func TestPlanOfAnUpdateShowsWhatWidened(t *testing.T) {
	s := newEnv(t).open(t)
	install(t, s, build("local/x", variant("a"), withKey(localManifest)))
	wider := strings.Replace(localManifest, "http.fetch: [192.168.1.5]", "http.fetch: [192.168.1.5, cloud.example.com]", 1)
	wider = strings.Replace(wider, "version: 1", "version: 2", 1)
	wider = strings.Replace(wider, "limits:\n  memory: 64MiB", "limits:\n  memory: 128MiB", 1)
	p := plan(t, s, build("local/x", variant("b"), withKey(wider)), noOwners)
	if !slices.Equal(p.Widening.Hosts, []string{"cloud.example.com"}) || p.Widening.Memory != 128<<20 || p.Widening.Timeout != 0 {
		t.Fatalf("widening %+v", p.Widening)
	}
	if len(p.Widening.Secrets)+len(p.Widening.CellsCreated)+len(p.Widening.Crons)+len(p.Widening.Tools) != 0 {
		t.Fatalf("widening %+v", p.Widening)
	}
	if len(p.Rights.Hosts) != 2 || p.Version != 2 {
		t.Fatalf("plan %+v", p)
	}
}

func TestCellsOfTheModuleAreAnUpdateNotARight(t *testing.T) {
	s := newEnv(t).open(t)
	owners := func(_ context.Context, id string) (string, bool, error) {
		switch id {
		case "x.soc":
			return "local/x", true, nil
		case "x.grid":
			return "", true, nil
		}
		return "", false, nil
	}
	p := plan(t, s, build("local/x", variant("a"), withKey(localManifest)), owners)
	if p.Refusal != "" || len(p.Rights.CellsCreated) != 0 || !slices.Equal(p.Rights.CellsTaken, []string{"x.grid"}) {
		t.Fatalf("plan %+v", p)
	}
}

func TestPlanRefusals(t *testing.T) {
	good := withKey(localManifest)
	other := func(context.Context, string) (string, bool, error) { return "local/other", true, nil }
	for name, c := range map[string]struct {
		module   string
		manifest string
		secret   func(string) bool
		config   map[string]any
		owners   modstore.CellOwners
		want     []string
	}{
		"built-in name":  {"ganglion/x", good, allSet, hostConfig, noOwners, []string{"local/"}},
		"reserved":       {"local/cells", good, allSet, hostConfig, noOwners, []string{"cells", "reserved"}},
		"empty name":     {"local/", good, allSet, hostConfig, noOwners, []string{"local/"}},
		"traversal":      {"local/../x", good, allSet, hostConfig, noOwners, []string{"name"}},
		"missing secret": {"local/x", good, func(string) bool { return false }, hostConfig, noOwners, []string{"INVERTER_KEY", "not set"}},
		"missing config": {"local/x", good, allSet, nil, noOwners, []string{"host", "required"}},
		"owned cell":     {"local/x", good, allSet, hostConfig, other, []string{"x.grid", "local/other"}},
		"bad manifest":   {"local/x", "name: x\nbogus: 1\n", allSet, hostConfig, noOwners, []string{"manifest"}},
		"another name":   {"local/x", strings.Replace(good, "name: x", "name: y", 1), allSet, hostConfig, noOwners, []string{"names another module"}},
		"tool no output": {"local/x", strings.Replace(good, "    output:\n      soc: number\n", "", 1), allSet, hostConfig, noOwners, []string{"soc", "output"}},
		"event emit":     {"local/x", strings.Replace(good, "  state: true\n", "  state: true\n  event.emit: [module]\n", 1), allSet, hostConfig, noOwners, []string{"event.emit"}},
		"abi":            {"local/x", strings.Replace(good, "abi: 1", "abi: 7", 1), allSet, hostConfig, noOwners, []string{"ABI"}},
	} {
		t.Run(name, func(t *testing.T) {
			s := newEnv(t).open(t)
			p, err := s.PlanLocal(context.Background(), build(c.module, variant("a"), c.manifest), c.secret, c.config, c.owners)
			if err != nil {
				t.Fatal(err)
			}
			if p.Refusal == "" {
				t.Fatalf("installable: %+v", p)
			}
			for _, w := range c.want {
				if !strings.Contains(p.Refusal, w) {
					t.Fatalf("refusal %q misses %q", p.Refusal, w)
				}
			}
			if strings.Contains(p.Refusal, "192.168.1.5") {
				t.Fatalf("a value in the refusal: %q", p.Refusal)
			}
		})
	}
}

func TestPlanRefusesWhatIsNotWasm(t *testing.T) {
	s := newEnv(t).open(t)
	p := plan(t, s, build("local/x", []byte("not wasm"), withKey(localManifest)), noOwners)
	if !strings.Contains(p.Refusal, "WebAssembly") {
		t.Fatalf("plan %+v", p)
	}
}

func TestPlanReportsAFailingOwnerLookup(t *testing.T) {
	s := newEnv(t).open(t)
	boom := errors.New("db down")
	_, err := s.PlanLocal(context.Background(), build("local/x", variant("a"), withKey(localManifest)), allSet, hostConfig,
		func(context.Context, string) (string, bool, error) { return "", false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err %v", err)
	}
}

func TestInstallOnlyThePlannedSha(t *testing.T) {
	e := newEnv(t)
	s := e.open(t)
	ctx := context.Background()
	b := build("local/x", variant("a"), withKey(localManifest))
	if _, err := s.InstallLocal(ctx, b, modstore.Digest([]byte("something else"), nil)); !errors.Is(err, modstore.ErrBuildMismatch) {
		t.Fatalf("a sha the store has not seen: %v", err)
	}
	if len(mustList(t, s)) != 0 {
		t.Fatal("a refused install left a row")
	}
	o := install(t, s, b)
	if o.Result != modstore.ResultInstalled || o.Module != "local/x" || o.To != "1" {
		t.Fatalf("outcome %+v", o)
	}
	if st, err := os.Stat(filepath.Join(e.dir, "local", "x", modstore.Digest(b.Wasm, b.Manifest)+".wasm")); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("stored file %v, %v", st, err)
	}
	if o := install(t, s, b); o.Result != modstore.ResultUpToDate {
		t.Fatalf("the same build again: %+v", o)
	}
}

// Review focus 2: a manifest with one more host and the digest of the plan
// of another build is refused, and nothing is activated.
func TestInstallRefusesAManifestThePlanDidNotShow(t *testing.T) {
	s := newEnv(t).open(t)
	a := build("local/x", variant("a"), withKey(localManifest))
	p := plan(t, s, a, noOwners)
	wider := a
	wider.Manifest = []byte(strings.Replace(withKey(localManifest), "http.fetch: [192.168.1.5]", "http.fetch: [192.168.1.5, evil.example.com]", 1))
	if _, err := s.InstallLocal(context.Background(), wider, p.SHA256); !errors.Is(err, modstore.ErrBuildMismatch) {
		t.Fatalf("err %v", err)
	}
	if len(mustList(t, s)) != 0 {
		t.Fatal("a refused install left a row")
	}
	if pw := plan(t, s, wider, noOwners); pw.SHA256 == p.SHA256 {
		t.Fatal("same wasm, other manifest, same digest")
	}
}

func TestInstallRefusesWhatThePlanWouldRefuse(t *testing.T) {
	s := newEnv(t).open(t)
	b := build("local/cells", variant("a"), withKey(localManifest))
	o, err := s.InstallLocal(context.Background(), b, modstore.Digest(b.Wasm, b.Manifest))
	if err != nil || o.Result != modstore.ResultRefused || !strings.Contains(o.Reason, "reserved") {
		t.Fatalf("outcome %+v, %v", o, err)
	}
	if len(mustList(t, s)) != 0 {
		t.Fatal("a refused install left a row")
	}
}

func TestVersionsRotateAndRetiredFilesGo(t *testing.T) {
	e := newEnv(t)
	s := e.open(t)
	var shas []string
	for i, tag := range []string{"a", "b", "c"} {
		b := build("local/x", variant(tag), strings.Replace(withKey(localManifest), "version: 1", "version: "+string(rune('1'+i)), 1))
		install(t, s, b)
		shas = append(shas, modstore.Digest(b.Wasm, b.Manifest))
	}
	states := map[string]modstore.State{}
	for _, v := range mustList(t, s) {
		states[v.SHA256] = v.State
	}
	if len(states) != 2 || states[shas[2]] != modstore.StateActive || states[shas[1]] != modstore.StatePrevious {
		t.Fatalf("states %v", states)
	}
	for i, want := range []bool{false, true, true} {
		_, err := os.Stat(filepath.Join(e.dir, "local", "x", shas[i]+".wasm"))
		if (err == nil) != want {
			t.Fatalf("file of version %d present=%v, want %v", i+1, err == nil, want)
		}
	}
}

func TestActiveLocal(t *testing.T) {
	s := newEnv(t).open(t)
	b1, b2 := build("local/x", variant("a"), withKey(localManifest)), build("local/y", variant("b"), strings.Replace(withKey(localManifest), "name: x", "name: y", 1))
	install(t, s, b1)
	install(t, s, b2)
	as, err := s.ActiveLocal(context.Background())
	if err != nil || len(as) != 2 {
		t.Fatalf("active %+v, %v", as, err)
	}
	if as[0].Module != "local/x" || as[0].SHA256 != modstore.Digest(b1.Wasm, b1.Manifest) || string(as[0].Wasm) != string(b1.Wasm) || string(as[0].Manifest) != string(b1.Manifest) {
		t.Fatalf("active[0] %+v", as[0])
	}
}

func TestActiveLocalSkipsAndMarksAVersionWhoseFilesChanged(t *testing.T) {
	e := newEnv(t)
	s := e.open(t)
	b := build("local/x", variant("a"), withKey(localManifest))
	install(t, s, b)
	if err := os.WriteFile(filepath.Join(e.dir, "local", "x", modstore.Digest(b.Wasm, b.Manifest)+".wasm"), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	as, err := s.ActiveLocal(context.Background())
	if err != nil || len(as) != 0 {
		t.Fatalf("active %+v, %v", as, err)
	}
	if v := mustList(t, s); len(v) != 1 || v[0].State != modstore.StateFailed {
		t.Fatalf("versions %+v", v)
	}
}

func TestJournalListsEveryVersionWithItsCounters(t *testing.T) {
	s := newEnv(t).open(t)
	ctx := context.Background()
	b1 := build("local/x", variant("a"), withKey(localManifest))
	b2 := build("local/x", variant("b"), strings.Replace(withKey(localManifest), "version: 1", "version: 2", 1))
	b3 := build("local/x", variant("c"), strings.Replace(withKey(localManifest), "version: 1", "version: 3", 1))
	b1.Note, b2.By, b2.Note, b2.Commit, b3.Note = "the first", "telegram:2", "a fix", "def456", "the third"
	for _, b := range []modstore.Build{b1, b2, b3} {
		install(t, s, b)
	}
	for range 3 {
		s.Count(ctx, "local/x", modstore.Digest(b3.Wasm, b3.Manifest), modstore.CallResult{})
	}
	s.Count(ctx, "local/x", modstore.Digest(b3.Wasm, b3.Manifest), modstore.CallResult{Failed: true, Kind: "module.format"})
	s.Count(ctx, "local/x", modstore.Digest(b3.Wasm, b3.Manifest), modstore.CallResult{Failed: true, Exception: true, Kind: "module.trap"})
	s.Count(ctx, "local/x", modstore.Digest(b1.Wasm, b1.Manifest), modstore.CallResult{})

	es, err := s.Journal(ctx, "local/x")
	if err != nil || len(es) != 3 {
		t.Fatalf("journal %+v, %v", es, err)
	}
	if es[0].Version != "3" || es[1].Version != "2" || es[2].Version != "1" {
		t.Fatalf("order %+v", es)
	}
	e := es[0]
	if e.SHA256 != modstore.Digest(b3.Wasm, b3.Manifest) || e.State != "active" || e.Note != "the third" || e.By != "telegram:1" || e.Commit != "abc123" ||
		e.Runs != 5 || e.Failures != 2 || e.Exceptions != 1 || e.LastFailureKind != "module.trap" || !e.LastFailure.Equal(t0) || !e.Installed.Equal(t0) {
		t.Fatalf("newest %+v", e)
	}
	if es[1].By != "telegram:2" || es[1].Commit != "def456" || es[1].Note != "a fix" || es[1].State != "previous" {
		t.Fatalf("middle %+v", es[1])
	}
	if es[2].State != "retired" || es[2].Runs != 1 || es[2].Failures != 0 || !es[2].LastFailure.IsZero() {
		t.Fatalf("oldest %+v", es[2])
	}
	if got, err := s.Journal(ctx, "local/none"); err != nil || len(got) != 0 {
		t.Fatalf("an unknown module: %+v, %v", got, err)
	}
}

func TestRollbackOfALocalModule(t *testing.T) {
	e := newEnv(t)
	s := e.open(t)
	ctx := context.Background()
	b1 := build("local/x", variant("a"), withKey(localManifest))
	b2 := build("local/x", variant("b"), strings.Replace(withKey(localManifest), "version: 1", "version: 2", 1))
	install(t, s, b1)
	install(t, s, b2)
	o, err := s.Rollback(ctx, "local/x")
	if err != nil || o.Result != modstore.ResultRolledBack || o.From != "2" || o.To != "1" {
		t.Fatalf("rollback %+v, %v", o, err)
	}
	as, err := s.ActiveLocal(ctx)
	if err != nil || len(as) != 1 || as[0].SHA256 != modstore.Digest(b1.Wasm, b1.Manifest) {
		t.Fatalf("active %+v, %v", as, err)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "local", "x", modstore.Digest(b2.Wasm, b2.Manifest)+".wasm")); err == nil {
		t.Fatal("the replaced version's file stayed")
	}
	if o, err := s.Rollback(ctx, "local/x"); err != nil || o.Result != modstore.ResultNothingToRollBack {
		t.Fatalf("second rollback %+v, %v", o, err)
	}
	if _, err := s.Rollback(ctx, "local/none"); !errors.Is(err, modstore.ErrUnknownModule) {
		t.Fatalf("an unknown module: %v", err)
	}
}

func TestListIncludesLocalModules(t *testing.T) {
	s := newEnv(t).open(t)
	install(t, s, build("local/x", variant("a"), withKey(localManifest)))
	found := false
	vs, _ := s.List(context.Background())
	for _, v := range vs {
		found = found || (v.Module == "local/x" && v.Source == modstore.SourceLocal && v.State == modstore.StateActive)
	}
	if !found {
		t.Fatalf("list %+v", vs)
	}
}
