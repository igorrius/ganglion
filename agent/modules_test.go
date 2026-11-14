package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/modstore"
)

// fakeModules records what /modules asked for.
type fakeModules struct {
	calls []string
}

func (f *fakeModules) List(context.Context) ([]modstore.Version, error) {
	f.calls = append(f.calls, "list")
	return []modstore.Version{
		{Module: "html2md", Version: "0.2.0", Source: "html2md/v0.2.0", State: modstore.StateActive},
		{Module: "html2md", Version: "builtin", Source: modstore.SourceBuiltin, State: modstore.StatePrevious},
		{Module: "html2md", Version: "0.3.0", Source: "html2md/v0.3.0", State: modstore.StateFailed, Reason: "the self-test failed: x"},
	}, nil
}

func (f *fakeModules) Update(_ context.Context, name string) (modstore.Outcome, error) {
	f.calls = append(f.calls, "update "+name)
	if name != "html2md" {
		return modstore.Outcome{}, modstore.ErrUnknownModule
	}
	return modstore.Outcome{Module: name, From: "builtin", To: "0.3.0", Result: modstore.ResultFailedSelfTest, Reason: "the self-test failed: trap"}, nil
}

func (f *fakeModules) Rollback(_ context.Context, name string) (modstore.Outcome, error) {
	f.calls = append(f.calls, "rollback "+name)
	return modstore.Outcome{Module: name, From: "0.2.0", To: "builtin", Result: modstore.ResultRolledBack}, nil
}

func modulesEnv(t *testing.T) (*env, *fakeModules) {
	t.Helper()
	e := newEnv(t, Config{}, nil)
	m := &fakeModules{}
	e.deps.Modules = m
	e.a = New(overDefaults(Config{}), e.deps)
	return e, m
}

// R-RUN-8: /modules lists, updates and rolls back, without a model, and
// says in plain words why an update was refused.
func TestModulesCommand(t *testing.T) {
	e, m := modulesEnv(t)
	for _, c := range [][2]string{
		{"/modules", "html2md: 0.2.0 (html2md/v0.2.0); previous: builtin; 0.3.0 failed: the self-test failed: x"},
		{"/modules update html2md", "html2md stays on builtin: the self-test failed: trap"},
		{"/modules rollback html2md", "html2md rolled back from 0.2.0 to builtin."},
		{"/modules update demo", "No module named demo."},
		{"/modules update", `Usage: /modules, /modules \<name\>, /modules config \<name\> key=value, /modules update, rollback, off, on or remove \<name\>`},
	} {
		text, want := c[0], c[1]
		out := e.turn(t, message(owner, "Igor", text))
		if len(out) != 1 || !strings.Contains(out[0].Text, want) {
			t.Errorf("%s: out %+v, want %q", text, out, want)
		}
	}
	if len(e.smart.requests()) != 0 {
		t.Fatal("a model was called")
	}
	if got := strings.Join(m.calls, ","); got != "list,update html2md,rollback html2md,update demo" {
		t.Fatalf("calls %s", got)
	}
}

func TestModulesCommandAdminOnly(t *testing.T) {
	e, m := modulesEnv(t)
	e.admin = false
	out := e.turn(t, message(owner, "Igor", "/modules update html2md"))
	if len(out) != 1 || out[0].Text != replyTasksNotAvailable || len(m.calls) != 0 {
		t.Fatalf("out %+v, calls %v", out, m.calls)
	}
}

// fakeLocal is the owner's modules as /modules sees them.
type fakeLocal struct {
	calls  []string
	card   modstore.Card
	config error
}

func (f *fakeLocal) Local(context.Context) ([]modstore.LocalModule, error) {
	return []modstore.LocalModule{{Name: "demo-reader", State: modstore.RunRunning, Version: "3"}, {Name: "boiler", State: modstore.RunCrashloop, Version: "1"}}, nil
}

func (f *fakeLocal) known(name string) error {
	if name != "demo-reader" && name != "local/demo-reader" {
		return modstore.ErrUnknownModule
	}
	return nil
}

func (f *fakeLocal) Card(_ context.Context, name string) (modstore.Card, error) {
	f.calls = append(f.calls, "card "+name)
	return f.card, f.known(name)
}

func (f *fakeLocal) Configure(_ context.Context, name, key, value string) error {
	f.calls = append(f.calls, "config "+name+" "+key+"="+value)
	if err := f.known(name); err != nil {
		return err
	}
	return f.config
}

func (f *fakeLocal) SetOff(_ context.Context, name string, off bool) error {
	f.calls = append(f.calls, fmt.Sprintf("off %s %v", name, off))
	return f.known(name)
}

func (f *fakeLocal) Rollback(_ context.Context, name string) (modstore.Outcome, error) {
	f.calls = append(f.calls, "rollback "+name)
	if err := f.known(name); err != nil {
		return modstore.Outcome{}, err
	}
	return modstore.Outcome{Module: "local/demo-reader", From: "3", To: "2", Result: modstore.ResultRolledBack}, nil
}

func (f *fakeLocal) Remove(_ context.Context, name string) error {
	f.calls = append(f.calls, "remove "+name)
	return f.known(name)
}

func ownModulesEnv(t *testing.T) (*env, *fakeLocal, *fakeModules) {
	t.Helper()
	e, m := modulesEnv(t)
	l := &fakeLocal{card: modstore.Card{
		LocalModule: modstore.LocalModule{Name: "demo-reader", State: modstore.RunRunning, Version: "3"},
		Health:      modstore.HealthOK,
		Journal: []modstore.Entry{
			{Version: "3", State: "active", Note: "temperature arrives as a string", Installed: time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC), Runs: 1340},
			{Version: "2", State: "previous", Note: "a *bold* note_", Installed: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), Runs: 200, Failures: 3},
			{Version: "1", State: "retired", Installed: time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC), Runs: 12},
		},
		Config: []modstore.ConfigLine{
			{Key: "site_id", Type: "number", Required: true, Value: "12345", Set: true},
			{Key: "region", Type: "string", Default: "eu"},
			{Key: "token_name", Type: "string"},
		},
	}}
	e.deps.Local = l
	e.a = New(overDefaults(Config{}), e.deps)
	return e, l, m
}

func oneReply(t *testing.T, e *env, text string) string {
	t.Helper()
	out := e.turn(t, message(owner, "Igor", text))
	if len(out) != 1 {
		t.Fatalf("%s: out %+v", text, out)
	}
	return out[0].Text
}

// The list names the built-in modules and the local ones with their state.
func TestModulesListsLocalModulesWithTheirState(t *testing.T) {
	e, _, _ := ownModulesEnv(t)
	want := "Modules:\n- html2md: 0.2.0 (html2md/v0.2.0); previous: builtin; 0.3.0 failed: the self-test failed: x\n" +
		"- demo-reader: running, version 3\n- boiler: off after failing 5 times in an hour, version 1"
	if got := oneReply(t, e, "/modules"); got != want {
		t.Fatalf("list:\n%s\nwant:\n%s", got, want)
	}
}

// The card is a golden text: the first install, every version with its
// counters, the settings; the owner's and the manifest's words are escaped
// and never noted for the model (R-RUN-10, R-RUN-13).
func TestModulesCardGolden(t *testing.T) {
	e, _, _ := ownModulesEnv(t)
	want := "demo-reader: running since 02.11\n" +
		"Health: ok\n" +
		"Versions:\n" +
		"v3  14.11  \"temperature arrives as a string\"  1,340 runs, 0 failures  ← active\n" +
		"v2  10.11  \"a \\*bold\\* note\\_\"  200 runs, 3 failures  ← previous\n" +
		"v1  02.11  12 runs, 0 failures  (retired)\n" +
		"Settings:\n" +
		"site\\_id: 12345 (number, required)\n" +
		"region: eu (default) (string)\n" +
		"token\\_name: not set (string)"
	if got := oneReply(t, e, "/modules demo-reader"); got != want {
		t.Fatalf("card:\n%s\nwant:\n%s", got, want)
	}
	for _, m := range e.state(t).Messages {
		if strings.Contains(m.Content, "bold") || strings.Contains(m.Content, "site_id") || strings.Contains(m.Content, "temperature") {
			t.Fatalf("the history holds the module's words: %q", m.Content)
		}
	}
	if len(e.smart.requests()) != 0 {
		t.Fatal("a model was called")
	}
}

// The card of a producer names the modules it feeds, the card of a listener
// the cells it reacts to and the messages it wrote today against its limit;
// a count over the limit shows the limit (R-RUN-15, R-RUN-16).
func TestModulesCardShowsReactionsAndMessages(t *testing.T) {
	e, l, _ := ownModulesEnv(t)
	l.card.Feeds = []string{"boiler", "a_b"}
	l.card.ReactsTo = []string{"grid.power", "x_y.z"}
	l.card.NotifyPerDay, l.card.NotifiedToday = 5, 7
	got := oneReply(t, e, "/modules demo-reader")
	for _, want := range []string{"Reacts to: grid.power, x\\_y.z\n", "Feeds: boiler, a\\_b\n", "Messages today: 5 of 5\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("card\n%s\nwant %q", got, want)
		}
	}
	l.card.Feeds, l.card.ReactsTo, l.card.NotifyPerDay = nil, nil, 0
	got = oneReply(t, e, "/modules demo-reader")
	if strings.Contains(got, "Reacts to") || strings.Contains(got, "Feeds") || strings.Contains(got, "Messages today") {
		t.Fatalf("a plain module shows reactions:\n%s", got)
	}
}

// The card tells a failed health check by its kind; a module that does not
// run has no health line.
func TestModulesCardHealth(t *testing.T) {
	for health, want := range map[modstore.Health]string{"auth_failed": "\nHealth: failed (auth\\_failed)\n", "": "02.11\nVersions:"} {
		e, l, _ := ownModulesEnv(t)
		l.card.Health = health
		if got := oneReply(t, e, "/modules demo-reader"); !strings.Contains(got, want) {
			t.Fatalf("health %q: card\n%s\nwant %q", health, got, want)
		}
	}
}

// Config refuses an undeclared key and a wrong type in the node's words, and
// neither the key nor the value is noted.
func TestModulesConfigAnswers(t *testing.T) {
	e, l, _ := ownModulesEnv(t)
	for _, c := range []struct {
		cmd, want string
		err       error
	}{
		{"/modules config demo-reader site_id=7", "demo-reader: site\\_id is set; it applies now.", nil},
		{"/modules config demo-reader nope=hunter2", "demo-reader has no setting nope. Its settings: site\\_id, region.",
			&modstore.ConfigError{Err: modstore.ErrUndeclaredKey, Key: "nope", Declared: []string{"site_id", "region"}}},
		{"/modules config demo-reader site_id=lots", "demo-reader: site\\_id takes a number value.",
			&modstore.ConfigError{Err: modstore.ErrWrongType, Key: "site_id", Type: "number"}},
		{"/modules config demo-reader site_id=1", "demo-reader did not start; see /status.", modstore.ErrNotStarted},
		{"/modules config ghost site_id=1", "No module named ghost.", nil},
		{"/modules config demo-reader site_id", `Usage: /modules, /modules \<name\>, /modules config \<name\> key=value, /modules update, rollback, off, on or remove \<name\>`, nil},
	} {
		l.config = c.err
		if got := oneReply(t, e, c.cmd); got != c.want {
			t.Errorf("%s:\n%s\nwant:\n%s", c.cmd, got, c.want)
		}
	}
	for _, m := range e.state(t).Messages {
		if strings.Contains(m.Content, "hunter2") || strings.Contains(m.Content, "site_id") {
			t.Fatalf("the history holds a key or a value: %q", m.Content)
		}
	}
	if !strings.Contains(strings.Join(l.calls, ";"), "config demo-reader nope=hunter2") {
		t.Fatalf("calls %v", l.calls)
	}
}

func TestModulesOffOnRemoveRollback(t *testing.T) {
	e, l, m := ownModulesEnv(t)
	for _, c := range [][2]string{
		{"/modules off demo-reader", "demo-reader is off."},
		{"/modules on demo-reader", "demo-reader is on."},
		{"/modules remove demo-reader", "demo-reader is removed. Its cells stay, read-only."},
		{"/modules rollback demo-reader", "demo-reader rolled back from 3 to 2."},
		{"/modules off ghost", "No module named ghost."},
		{"/modules remove ghost", "No module named ghost."},
		{"/modules rollback html2md", "html2md rolled back from 0.2.0 to builtin."},
	} {
		if got := oneReply(t, e, c[0]); got != c[1] {
			t.Errorf("%s: %q, want %q", c[0], got, c[1])
		}
	}
	if got := strings.Join(l.calls, ";"); got != "off demo-reader true;off demo-reader false;remove demo-reader;rollback demo-reader;off ghost true;remove ghost;rollback html2md" {
		t.Fatalf("local calls %s", got)
	}
	if got := strings.Join(m.calls, ","); got != "rollback html2md" {
		t.Fatalf("built-in calls %s", got)
	}
}

func TestGroupedNumbers(t *testing.T) {
	for n, want := range map[int]string{0: "0", 999: "999", 1000: "1,000", 1340: "1,340", 1234567: "1,234,567"} {
		if got := grouped(n); got != want {
			t.Errorf("%d: %s", n, got)
		}
	}
}
