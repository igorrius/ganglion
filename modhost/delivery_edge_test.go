package modhost_test

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/edge"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/sqlitedb"
)

// This file is an edge taking what the core delivers (R-EDGE-6, ADR-0117):
// the edge's real module store and host, its converger, and a core that is
// a map of files and of what the edge reported. The bus between them is
// natsbus's own tests'.

// fakeCore is the core as an edge sees it: the files it holds by digest,
// and what the edge says it runs.
type fakeCore struct {
	mu      sync.Mutex
	files   map[string][2][]byte // digest → the .wasm and the manifest
	actual  map[string]edge.Actual
	fetches int
}

func newFakeCore() *fakeCore {
	return &fakeCore{files: map[string][2][]byte{}, actual: map[string]edge.Actual{}}
}

// offer puts b's files under its digest.
func (c *fakeCore) offer(b modstore.Build) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	d := modstore.Digest(b.Wasm, b.Manifest)
	c.files[d] = [2][]byte{b.Wasm, b.Manifest}
	return d
}

func (c *fakeCore) fetch(_ context.Context, module, digest string, _ int64) ([]byte, []byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fetches++
	f, ok := c.files[digest]
	if !ok || module != "probe" {
		return nil, nil, errors.New("no such object")
	}
	return f[0], f[1], nil
}

func (c *fakeCore) report(_ context.Context, edgeName, module string, value []byte) error {
	var a edge.Actual
	if err := json.Unmarshal(value, &a); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.actual[edgeName+"."+module] = a
	return nil
}

func (c *fakeCore) said(t *testing.T) edge.Actual {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.actual["e1.probe"]
	if !ok {
		t.Fatal("the edge said nothing of probe")
	}
	return a
}

func (c *fakeCore) fetched() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fetches
}

// edgeEnv is the edge e1 with a real module store and the converger.
type edgeEnv struct {
	*env
	modules *modstore.Store
	core    *fakeCore
	conv    *edge.Converger
}

func newEdgeEnv(t *testing.T) *edgeEnv {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "edge.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	ms, err := modstore.Open(ctx, db, filepath.Join(t.TempDir(), "modules"), time.Now, runner.Deps{})
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t)
	e.modules, e.node = ms, "e1"
	e.host = e.newHost()
	e.start(t)
	core := newFakeCore()
	return &edgeEnv{env: e, modules: ms, core: core, conv: &edge.Converger{
		Edge: "e1", Fetch: core.fetch, Report: core.report, Host: e.host, Store: ms, Lines: e.conf,
	}}
}

// version is the probe's build b at version n.
func version(t *testing.T, manifest string, n string) modstore.Build {
	t.Helper()
	return build(t, strings.Replace(manifest, "version: 3", "version: "+n, 1))
}

// told is the update of e1's desired key of probe naming digest at version.
func told(t *testing.T, digest, ver string) edge.Update {
	t.Helper()
	return toldWith(t, digest, ver, nil)
}

// toldWith is told with the module's config.
func toldWith(t *testing.T, digest, ver string, cfg map[string]any) edge.Update {
	t.Helper()
	v, err := json.Marshal(edge.Desired{V: edge.DeliveryVersion, Module: "probe", Version: ver, SHA256: digest, Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return edge.Update{Edge: "e1", Module: "probe", Value: v}
}

// running is the version of probe the edge's host runs; "" when none.
func (e *edgeEnv) running(t *testing.T) string {
	t.Helper()
	ms, err := e.host.Local(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.Name == "probe" && m.State == modstore.RunRunning {
			return m.Version
		}
	}
	return ""
}

// apply hands the converger u, as the watch does.
func (e *edgeEnv) apply(u edge.Update) { e.conv.Apply(context.Background(), u) }

// The P2 gate on the edge's side: a version the core delivers runs and is
// reported, an update replaces it, a rollback brings the one before back,
// and a withdrawal takes it out of service.
func TestEdgeRunsUpdatesAndRollsBackWhatTheCoreDelivers(t *testing.T) {
	e := newEdgeEnv(t)
	v3, v4 := version(t, withLifecycle("ok"), "3"), version(t, withLifecycle("ok"), "4")
	d3, d4 := e.core.offer(v3), e.core.offer(v4)

	e.apply(told(t, d3, "3"))
	if got := e.running(t); got != "3" {
		t.Fatalf("running %q, want 3", got)
	}
	if a := e.core.said(t); a.Result != modstore.EdgeRunning || a.SHA256 != d3 || a.Desired != d3 || a.Version != "3" {
		t.Fatalf("actual %+v", a)
	}

	e.apply(told(t, d4, "4"))
	if got := e.running(t); got != "4" {
		t.Fatalf("after the update running %q, want 4", got)
	}
	if a := e.core.said(t); a.Result != modstore.EdgeRunning || a.SHA256 != d4 {
		t.Fatalf("actual after the update %+v", a)
	}

	// Rollback is the core writing the previous version to desired.
	e.apply(told(t, d3, "3"))
	if got := e.running(t); got != "3" {
		t.Fatalf("after the rollback running %q, want 3", got)
	}
	if a := e.core.said(t); a.Result != modstore.EdgeRunning || a.SHA256 != d3 || a.Version != "3" {
		t.Fatalf("actual after the rollback %+v", a)
	}

	// The same key again changes nothing and fetches nothing.
	n := e.core.fetched()
	e.apply(told(t, d3, "3"))
	if e.core.fetched() != n {
		t.Fatal("a version that runs was fetched again")
	}

	e.apply(edge.Update{Edge: "e1", Module: "probe", Deleted: true})
	if got := e.running(t); got != "" {
		t.Fatalf("after the withdrawal running %q", got)
	}
	if a := e.core.said(t); a.Result != modstore.EdgeRemoved || a.SHA256 != "" {
		t.Fatalf("actual after the withdrawal %+v", a)
	}
}

// A failed health keeps the old version: the new one is refused before it
// is installed, the refusal is reported in the node's words, and the same
// digest is not fetched again.
func TestEdgeKeepsTheOldVersionWhenHealthFails(t *testing.T) {
	e := newEdgeEnv(t)
	d3 := e.core.offer(version(t, withLifecycle("ok"), "3"))
	bad := e.core.offer(version(t, withLifecycle("health_auth"), "4"))
	e.apply(told(t, d3, "3"))

	e.apply(told(t, bad, "4"))
	if got := e.running(t); got != "3" {
		t.Fatalf("running %q, want the old 3", got)
	}
	a := e.core.said(t)
	if a.Result != modstore.EdgeRefused || a.Desired != bad || a.SHA256 != d3 || !strings.Contains(a.Reason, "health check failed (auth_failed)") {
		t.Fatalf("actual %+v", a)
	}
	if strings.Contains(a.Reason, "ignore previous") {
		t.Fatalf("the module's text reached the reason: %q", a.Reason)
	}
	n := e.core.fetched()
	e.apply(told(t, bad, "4"))
	if e.core.fetched() != n {
		t.Fatal("a refused digest was fetched again")
	}
	vs, err := e.modules.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if v.SHA256 == bad {
			t.Fatalf("the refused version was stored: %+v", v)
		}
	}
}

// A version that passes its health but does not start (here: it reports a
// cell another module owns) is rolled back to the one before it.
func TestEdgeRollsBackAVersionThatDoesNotStart(t *testing.T) {
	e := newEdgeEnv(t)
	d3 := e.core.offer(version(t, withLifecycle("ok"), "3"))
	e.apply(told(t, d3, "3"))
	if err := e.cells.DefineOwned(context.Background(), cells.OwnedDef{ID: "probe.taken", Owner: "local/other", Kind: cells.KindSensor}); err != nil {
		t.Fatal(err)
	}
	taken := strings.Replace(withLifecycle("ok"), "  report:\n", "  report:\n    taken: {kind: sensor, type: integer, about: \"Taken\"}\n", 1)
	d4 := e.core.offer(version(t, taken, "4"))

	e.apply(told(t, d4, "4"))
	if got := e.running(t); got != "3" {
		t.Fatalf("running %q, want 3 back", got)
	}
	if a := e.core.said(t); a.Result != modstore.EdgeRefused || a.SHA256 != d3 || a.Desired != d4 || a.Reason == "" {
		t.Fatalf("actual %+v", a)
	}
}

// A new version that brings a new config and does not start leaves the
// version before running with its own config, in the file as well.
func TestEdgeKeepsTheOldConfigWhenTheNewVersionDoesNotStart(t *testing.T) {
	e := newEdgeEnv(t)
	oldCfg := map[string]any{"op_tool_status": `{"op":"now"}`}
	d3 := e.core.offer(version(t, withLifecycle("ok"), "3"))
	e.apply(toldWith(t, d3, "3", oldCfg))
	if err := e.cells.DefineOwned(context.Background(), cells.OwnedDef{ID: "probe.taken", Owner: "local/other", Kind: cells.KindSensor}); err != nil {
		t.Fatal(err)
	}
	taken := strings.Replace(withLifecycle("ok"), "  report:\n", "  report:\n    taken: {kind: sensor, type: integer, about: \"Taken\"}\n", 1)
	d4 := e.core.offer(version(t, taken, "4"))

	e.apply(toldWith(t, d4, "4", map[string]any{"op_tool_status": `{"op":"echo"}`}))
	if got := e.running(t); got != "3" {
		t.Fatalf("running %q, want 3 back", got)
	}
	line := func() any {
		mods, _ := e.conf.Modules()
		for _, m := range mods {
			if m.Name == "local/probe" {
				return m.Config["op_tool_status"]
			}
		}
		return nil
	}
	if got := line(); got != `{"op":"now"}` {
		t.Fatalf("the line holds %v, want the old config", got)
	}
	// Told v3 with its config again (a rollback on the core), nothing is
	// rewritten: the config it runs with is the old one.
	e.apply(toldWith(t, d3, "3", oldCfg))
	if got := line(); got != `{"op":"now"}` || e.running(t) != "3" {
		t.Fatalf("after v3 again: line %v, running %q", got, e.running(t))
	}
	// With the new config the status tool would break its contract.
	if res, err := e.call(t, "probe.status"); err != nil {
		t.Fatalf("v3 does not answer with its old config: %+v %v", res, err)
	}
}

// Nothing the edge did not check runs: files that do not match the digest
// in the desired key (a wrong digest, or a .wasm changed after the core
// wrote it), a desired key of another edge and a value that is not one
// are all refused, and the version that ran keeps running.
func TestEdgeRefusesWhatItCannotTrust(t *testing.T) {
	e := newEdgeEnv(t)
	d3 := e.core.offer(version(t, withLifecycle("ok"), "3"))
	e.apply(told(t, d3, "3"))
	v4 := version(t, withLifecycle("ok"), "4")
	d4 := e.core.offer(v4)

	// The desired key names a digest; the files under it are another build.
	wrong := strings.Repeat("ab", 32)
	e.core.mu.Lock()
	e.core.files[wrong] = [2][]byte{v4.Wasm, v4.Manifest}
	e.core.mu.Unlock()
	e.apply(told(t, wrong, "4"))
	if a := e.core.said(t); a.Result != modstore.EdgeRefused || a.Desired != wrong || !strings.Contains(a.Reason, "do not match the digest") {
		t.Fatalf("a wrong digest: actual %+v", a)
	}

	// The .wasm under the right digest is changed after the core wrote it.
	tampered := append([]byte{}, v4.Wasm...)
	tampered[len(tampered)-1] ^= 0xff
	e.core.mu.Lock()
	e.core.files[d4] = [2][]byte{tampered, v4.Manifest}
	e.core.mu.Unlock()
	e.apply(told(t, d4, "4"))
	if a := e.core.said(t); a.Result != modstore.EdgeRefused || a.Desired != d4 || !strings.Contains(a.Reason, "do not match the digest") {
		t.Fatalf("a tampered .wasm: actual %+v", a)
	}

	// A desired key of another edge is not acted on, nor answered.
	e.core.mu.Lock()
	e.core.files[d4] = [2][]byte{v4.Wasm, v4.Manifest}
	e.core.mu.Unlock()
	n, before := e.core.fetched(), e.core.said(t)
	other := told(t, d4, "4")
	other.Edge = "e2"
	e.apply(other)
	if e.core.fetched() != n || e.core.said(t) != before {
		t.Fatal("e1 acted on a desired key of e2")
	}

	// A value of another module, or with a digest that is not one, is
	// refused before anything is fetched.
	for _, d := range []edge.Desired{
		{V: edge.DeliveryVersion, Module: "other", Version: "4", SHA256: d4},
		{V: edge.DeliveryVersion, Module: "probe", Version: "4", SHA256: "../../etc"},
		{V: 99, Module: "probe", Version: "4", SHA256: d4},
	} {
		v, _ := json.Marshal(d)
		e.apply(edge.Update{Edge: "e1", Module: "probe", Value: v})
		if a := e.core.said(t); a.Result != modstore.EdgeRefused || a.Desired != "" {
			t.Fatalf("value %+v: actual %+v", d, a)
		}
	}
	if e.core.fetched() != n {
		t.Fatal("a value the edge does not read made it fetch")
	}
	if got := e.running(t); got != "3" {
		t.Fatalf("running %q, want 3 throughout", got)
	}
	vs, err := e.modules.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].SHA256 != d3 {
		t.Fatalf("the store holds %+v, want 3 alone", vs)
	}
}

// fakeDelivery records what the core's host delivers and withdraws.
type fakeDelivery struct {
	mu        sync.Mutex
	delivered []string // "edge:module:digest"
	withdrawn []string // "module:keep"
	run       modstore.EdgeRun
}

func (d *fakeDelivery) Deliver(_ context.Context, e string, a modstore.Active, _ map[string]any) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.delivered = append(d.delivered, e+":"+a.Module+":"+a.SHA256)
	return nil
}

func (d *fakeDelivery) Withdraw(_ context.Context, module, keep string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.withdrawn = append(d.withdrawn, module+":"+keep)
	return nil
}

func (d *fakeDelivery) Actual(context.Context, string, string) (modstore.EdgeRun, bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.run, d.run.Result != "", nil
}

// On the core, a module whose line names an edge is delivered there and not
// run; its check before the install only loads it (its health is the
// edge's); turned off, it is withdrawn; /modules shows how the edge stands.
func TestCoreDeliversAModuleItsLineSendsToAnEdge(t *testing.T) {
	ctx := context.Background()
	a := active(t, "local/probe", withLifecycle("ok"))
	e := newEnv(t, a)
	d := &fakeDelivery{}
	e.delivery = d
	e.conf.mods = []settings.Module{{Name: "local/probe", Node: "e1"}}
	e.host = e.newHost()
	e.start(t)

	if len(e.host.Handlers()) != 0 || len(e.jobs.all()) != 0 {
		t.Fatal("the core runs a module its line sends to an edge")
	}
	if want := "e1:local/probe:" + a.SHA256; len(d.delivered) != 1 || d.delivered[0] != want {
		t.Fatalf("delivered %v, want %s", d.delivered, want)
	}
	if len(d.withdrawn) != 1 || d.withdrawn[0] != "probe:e1" {
		t.Fatalf("withdrawn %v: every other edge's key goes", d.withdrawn)
	}
	if err := e.host.Check(ctx, build(t, withLifecycle("health_auth")), nil); err != nil {
		t.Fatalf("the core checked the health of an edge's module: %v", err)
	}

	d.run = modstore.EdgeRun{Version: "3", SHA256: a.SHA256, Desired: a.SHA256, Result: modstore.EdgeRunning}
	ms, err := e.host.Local(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].State != modstore.RunEdge || ms[0].Edge == nil || ms[0].Edge.Node != "e1" || ms[0].Edge.Result != modstore.EdgeRunning {
		t.Fatalf("Local = %+v", ms)
	}
	d.run.Desired = strings.Repeat("0", 64) // the edge still answers for an older version
	if ms, _ := e.host.Local(ctx); ms[0].Edge.Result != modstore.EdgeWaiting {
		t.Fatalf("an edge that has not taken the active version: %+v", ms[0].Edge)
	}

	if err := e.conf.SetOff("local/probe", true, ""); err != nil {
		t.Fatal(err)
	}
	if err := e.host.Reload(ctx, "local/probe"); err != nil {
		t.Fatal(err)
	}
	if last := d.withdrawn[len(d.withdrawn)-1]; last != "probe:" {
		t.Fatalf("turned off, it is not withdrawn from every edge: %v", d.withdrawn)
	}
	if ms, _ := e.host.Local(ctx); ms[0].State != modstore.RunOff {
		t.Fatalf("turned off, it shows %+v", ms[0])
	}
}

// The owner's one command, module.rollback, on a module that runs on an
// edge: the core delivers the previous version's digest to that edge's
// desired key and runs nothing itself (P2 gate, #324).
func TestRollbackOfAnEdgeModuleDeliversThePreviousVersion(t *testing.T) {
	ctx := context.Background()
	v4 := active(t, "local/probe", withLifecycle("ok"))
	v3 := active(t, "local/probe", withLifecycle("health_auth"))
	if v3.SHA256 == v4.SHA256 {
		t.Fatal("the two versions have one digest")
	}
	e := newEnv(t, v4)
	d := &fakeDelivery{}
	e.delivery = d
	e.store.previous = map[string]modstore.Active{"local/probe": v3}
	e.conf.mods = []settings.Module{{Name: "local/probe", Node: "e1"}}
	e.host = e.newHost()
	e.start(t)

	if _, err := e.host.Rollback(ctx, "probe"); err != nil {
		t.Fatal(err)
	}
	want := []string{"e1:local/probe:" + v4.SHA256, "e1:local/probe:" + v3.SHA256}
	if !slices.Equal(d.delivered, want) {
		t.Fatalf("delivered %v, want %v", d.delivered, want)
	}
	if len(e.host.Handlers()) != 0 {
		t.Fatal("the core runs a module that is on an edge")
	}
}
