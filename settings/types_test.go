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

func TestMoneyRefusesNaNAndInf(t *testing.T) {
	for _, v := range []string{"NaN", "Inf", "+Inf", ".nan", ".inf", "infinity"} {
		_, err := settings.Parse([]byte("agent:\n  limits: {daily_budget_usd: " + v + "}\n"))
		if err == nil || !strings.Contains(err.Error(), "agent.limits.daily_budget_usd") {
			t.Errorf("%s: err = %v", v, err)
		}
	}
}
