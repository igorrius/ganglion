package modhost_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/settings"
)

// typedManifest is the probe with a number and a boolean key beside the
// string ones.
var typedManifest = strings.Replace(probeManifest, "config:\n", "config:\n  limit: {type: number}\n  loud: {type: boolean, required: true}\n", 1)

func startedProbe(t *testing.T) *env {
	t.Helper()
	e := newEnv(t, active(t, "local/probe", typedManifest))
	e.start(t)
	return e
}

// The list and the card name the state; the card carries the declared keys
// with what is set now (R-RUN-10).
func TestLocalAndCard(t *testing.T) {
	e := startedProbe(t)
	ctx := context.Background()
	ls, err := e.host.Local(ctx)
	if err != nil || len(ls) != 1 || ls[0] != (modstore.LocalModule{Name: "probe", State: modstore.RunRunning, Version: "3"}) {
		t.Fatalf("local %+v, %v", ls, err)
	}
	if err := e.host.Configure(ctx, "probe", "limit", "5"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"probe", "local/probe"} {
		c, err := e.host.Card(ctx, name)
		if err != nil || c.Name != "probe" || len(c.Journal) != 1 || c.Journal[0].State != "active" {
			t.Fatalf("card %q: %+v, %v", name, c, err)
		}
		var limit, loud modstore.ConfigLine
		for _, l := range c.Config {
			switch l.Key {
			case "limit":
				limit = l
			case "loud":
				loud = l
			}
		}
		if limit != (modstore.ConfigLine{Key: "limit", Type: "number", Value: "5", Set: true}) || loud != (modstore.ConfigLine{Key: "loud", Type: "boolean", Required: true}) {
			t.Fatalf("config %+v %+v", limit, loud)
		}
	}
	if _, err := e.host.Card(ctx, "nope"); !errors.Is(err, modstore.ErrUnknownModule) {
		t.Fatalf("unknown: %v", err)
	}
}

// Config refuses an undeclared key and a value of the wrong type, and the
// error never holds the value; a good value is written typed and the module
// reloaded.
func TestConfigureValidatesAgainstTheManifest(t *testing.T) {
	e := startedProbe(t)
	ctx := context.Background()
	err := e.host.Configure(ctx, "probe", "nope", "hunter2")
	var ce *modstore.ConfigError
	if !errors.As(err, &ce) || !errors.Is(err, modstore.ErrUndeclaredKey) || ce.Key != "nope" || len(ce.Declared) == 0 || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("undeclared: %v", err)
	}
	for _, bad := range [][2]string{{"limit", "lots"}, {"limit", "NaN"}, {"loud", "yes"}} {
		err := e.host.Configure(ctx, "probe", bad[0], bad[1])
		if !errors.Is(err, modstore.ErrWrongType) || strings.Contains(err.Error(), bad[1]) {
			t.Errorf("%v: %v", bad, err)
		}
	}
	if len(e.conf.mods) != 0 {
		t.Fatalf("a refusal wrote %+v", e.conf.mods)
	}
	for _, ok := range [][2]string{{"limit", "5"}, {"limit", "2.5"}, {"loud", "true"}, {"op_cron", `{"op":"now"}`}} {
		if err := e.host.Configure(ctx, "probe", ok[0], ok[1]); err != nil {
			t.Fatalf("%v: %v", ok, err)
		}
	}
	got := e.conf.mods[0].Config
	if got["limit"] != 2.5 || got["loud"] != true || got["op_cron"] != `{"op":"now"}` {
		t.Fatalf("config %#v", got)
	}
	if err := e.host.Configure(ctx, "other", "limit", "1"); !errors.Is(err, modstore.ErrUnknownModule) {
		t.Fatalf("unknown module: %v", err)
	}
}

// Off writes off: true and stops the module; on removes it and runs it again.
func TestSetOffAndOn(t *testing.T) {
	e := startedProbe(t)
	ctx := context.Background()
	if err := e.host.SetOff(ctx, "probe", true); err != nil {
		t.Fatal(err)
	}
	if !e.conf.mods[0].Off || len(e.host.Handlers()) != 0 || len(e.jobs.all()) != 0 {
		t.Fatalf("off: %+v, %d tools, %d jobs", e.conf.mods, len(e.host.Handlers()), len(e.jobs.all()))
	}
	if ls, _ := e.host.Local(ctx); ls[0].State != modstore.RunOff {
		t.Fatalf("state %+v", ls)
	}
	if err := e.host.SetOff(ctx, "probe", false); err != nil {
		t.Fatal(err)
	}
	if e.conf.mods[0].Off || len(e.host.Handlers()) == 0 || len(e.jobs.all()) != 1 {
		t.Fatalf("on: %+v", e.conf.mods)
	}
	if ls, _ := e.host.Local(ctx); ls[0].State != modstore.RunRunning {
		t.Fatalf("state %+v", ls)
	}
	if err := e.host.SetOff(ctx, "nope", true); !errors.Is(err, modstore.ErrUnknownModule) {
		t.Fatalf("unknown: %v", err)
	}
}

// A module the settings turn off reads as off after a reload.
func TestOffInTheSettingsReadsAsOff(t *testing.T) {
	e := startedProbe(t)
	ctx := context.Background()
	e.conf.mods = []settings.Module{{Name: "local/probe", Off: true}}
	if err := e.host.Reload(ctx, "local/probe"); err != nil {
		t.Fatal(err)
	}
	if ls, _ := e.host.Local(ctx); ls[0].State != modstore.RunOff {
		t.Fatalf("state %+v", ls)
	}
}

// Rollback reloads the module; remove stops it, drops its line and jobs and
// frees its cells, which stay.
func TestRollbackAndRemove(t *testing.T) {
	e := startedProbe(t)
	ctx := context.Background()
	o, err := e.host.Rollback(ctx, "probe")
	if err != nil || o.Result != modstore.ResultRolledBack || len(e.store.rolledBack) != 1 || e.store.rolledBack[0] != "local/probe" {
		t.Fatalf("rollback %+v, %v", o, err)
	}
	if _, err := e.host.Rollback(ctx, "nope"); !errors.Is(err, modstore.ErrUnknownModule) {
		t.Fatalf("unknown: %v", err)
	}
	if err := e.host.Configure(ctx, "probe", "limit", "1"); err != nil {
		t.Fatal(err)
	}
	if err := e.host.Remove(ctx, "probe"); err != nil {
		t.Fatal(err)
	}
	if len(e.conf.mods) != 0 || len(e.jobs.all()) != 0 || len(e.host.Handlers()) != 0 {
		t.Fatalf("remove left %+v, %d jobs", e.conf.mods, len(e.jobs.all()))
	}
	c, err := e.cells.Get(ctx, "probe.soc")
	if err != nil || c.Owner != "" {
		t.Fatalf("the cell stays, ownerless: %+v, %v", c, err)
	}
	if ls, _ := e.host.Local(ctx); len(ls) != 0 {
		t.Fatalf("list %+v", ls)
	}
	if err := e.host.Remove(ctx, "probe"); !errors.Is(err, modstore.ErrUnknownModule) {
		t.Fatalf("second remove: %v", err)
	}
}
