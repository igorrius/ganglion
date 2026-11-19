package settings_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/settings"
)

func TestSecretRefRejectsLiterals(t *testing.T) {
	_, err := settings.Parse([]byte("telegram:\n  token: 123:ABC\n"))
	if err == nil || !strings.Contains(err.Error(), "telegram.token") || strings.Contains(err.Error(), "123:ABC") {
		t.Fatalf("err = %v; want a path and no value", err)
	}
}

func TestSecretRefTakesEnvAndStore(t *testing.T) {
	for ref, want := range map[string]settings.Scheme{
		"env:TELEGRAM_BOT_TOKEN": settings.SchemeEnv, "store:TELEGRAM_BOT_TOKEN": settings.SchemeStore,
	} {
		f, err := settings.Parse([]byte("telegram:\n  token: " + ref + "\n"))
		if err != nil {
			t.Fatalf("%s: %v", ref, err)
		}
		if tok := f.Telegram.Token; tok.Scheme() != want || tok.Name() != "TELEGRAM_BOT_TOKEN" {
			t.Errorf("%s: scheme %q, name %q", ref, tok.Scheme(), tok.Name())
		}
	}
	for _, ref := range []string{"store:", "store:lower", "vault:X", "env:", "S3CR3T-literal", "store:A:B"} {
		_, err := settings.Parse([]byte("telegram:\n  token: \"" + ref + "\"\n"))
		switch {
		case err == nil || !strings.Contains(err.Error(), "must be env:NAME or store:NAME"):
			t.Errorf("%s: err = %v", ref, err)
		case ref != "store:" && ref != "env:" && strings.Contains(err.Error(), ref):
			t.Errorf("%s: the error repeats the value: %v", ref, err)
		}
	}
	if s := settings.SecretRef("").Scheme(); s != "" {
		t.Fatalf("the scheme of an empty reference = %q", s)
	}
}

// unasked is a source a reference of another scheme must not read.
type unasked struct{ t *testing.T }

func (u unasked) Secret(name string) (string, error) {
	u.t.Errorf("the other scheme's source was asked for %s", name)
	return "", nil
}

func TestSecretRefResolvesFromItsSchemesSource(t *testing.T) {
	has := fakeSource{"BOT": "t0k"}
	for ref, src := range map[settings.SecretRef]settings.Sources{
		"env:BOT":   {Env: has, Store: unasked{t}},
		"store:BOT": {Env: unasked{t}, Store: has},
	} {
		if got, err := ref.Resolve(src); err != nil || got != "t0k" {
			t.Errorf("%s: Resolve = %q, %v", ref, got, err)
		}
	}
	if got, err := settings.SecretRef("").Resolve(settings.Sources{Env: unasked{t}, Store: unasked{t}}); err != nil || got != "" {
		t.Fatalf("an empty reference: Resolve = %q, %v", got, err)
	}
}

func TestSecretNamesAndStoreNamesSplitByScheme(t *testing.T) {
	f, err := settings.Parse([]byte(`
telegram: {token: "store:BOT"}
agent: {search: {brave_key: "env:BRAVE"}}
models:
  providers:
    - {name: a, base_url: "https://a.example/v1", key: "env:A_KEY"}
    - {name: b, base_url: "https://b.example/v1", key: "store:B_KEY"}
  models:
    - {id: m, provider: a, name: m, tools: true, context: 200000}
  purposes:
    chat:  [{model: m}]
    check: [{model: m}]
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := f.SecretNames(); !slices.Equal(got, []string{"BRAVE", "A_KEY"}) {
		t.Errorf("SecretNames = %v", got)
	}
	if got := f.StoreNames(); !slices.Equal(got, []string{"BOT", "B_KEY"}) {
		t.Errorf("StoreNames = %v", got)
	}
	if got := f.Models.KeyNames(); !slices.Equal(got, []string{"A_KEY"}) {
		t.Errorf("KeyNames = %v", got)
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

func TestMoneyRefusesNaNAndInf(t *testing.T) {
	for _, v := range []string{"NaN", "Inf", "+Inf", ".nan", ".inf", "infinity"} {
		_, err := settings.Parse([]byte("agent:\n  limits: {daily_budget_usd: " + v + "}\n"))
		if err == nil || !strings.Contains(err.Error(), "agent.limits.daily_budget_usd") {
			t.Errorf("%s: err = %v", v, err)
		}
	}
}

// ADR-0100: the turn's step budget is a setting, 20 by default, 1 to 100.
func TestMaxSteps(t *testing.T) {
	if got := settings.Default().Agent.MaxSteps; got != 20 {
		t.Fatalf("default %d", got)
	}
	f, err := settings.Parse([]byte("agent:\n  max_steps: 35\n"))
	if err != nil || f.Agent.MaxSteps != 35 {
		t.Fatalf("got %+v, %v", f.Agent, err)
	}
	for _, src := range []string{"agent:\n  max_steps: 0\n", "agent:\n  max_steps: 101\n", "agent:\n  max_steps: -3\n", "agent:\n  max_steps: many\n"} {
		if _, err := settings.Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), "agent.max_steps") {
			t.Errorf("%q: err = %v", src, err)
		}
	}
}

// Ruling 17 of M4c: a model call gets 3 min and a turn 5 min by default;
// the node's handler gets the turn's plus a minute, which must stay below
// the queue's lease, or the node does not start.
func TestTurnTimeouts(t *testing.T) {
	d := settings.Default().Agent
	if time.Duration(d.CallTimeout) != 3*time.Minute || time.Duration(d.TurnTimeout) != 5*time.Minute || d.HandlerTimeout() != 6*time.Minute {
		t.Fatalf("defaults %v, %v, %v", d.CallTimeout, d.TurnTimeout, d.HandlerTimeout())
	}
	f, err := settings.Parse([]byte("agent:\n  call_timeout: 90s\n  turn_timeout: 8m\n"))
	if err != nil {
		t.Fatal(err)
	}
	if time.Duration(f.Agent.CallTimeout) != 90*time.Second || f.Agent.HandlerTimeout() != 9*time.Minute {
		t.Fatalf("got %+v", f.Agent)
	}
	for src, path := range map[string]string{
		"agent:\n  turn_timeout: 9m\n":                     "agent.turn_timeout",
		"agent:\n  turn_timeout: 1h\n":                     "agent.turn_timeout",
		"agent:\n  turn_timeout: off\n":                    "agent.turn_timeout",
		"agent:\n  call_timeout: off\n":                    "agent.call_timeout",
		"agent:\n  call_timeout: 6m\n  turn_timeout: 5m\n": "agent.call_timeout",
		"agent:\n  call_timeout: soon\n":                   "agent.call_timeout",
	} {
		if _, err := settings.Parse([]byte(src)); err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("%q: err = %v, want %s", src, err, path)
		}
	}
}
