package settings_test

import (
	"slices"
	"testing"
	"time"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/settings"
)

func TestDefaultsMatchTheAgent(t *testing.T) {
	a, d := agent.DefaultConfig(), settings.Default().Agent
	if int(d.Limits.DailyTurns) != a.DailyTurns || int(d.Limits.DailyTokens) != a.DailyTokens || time.Duration(d.SessionIdle) != a.SessionIdle {
		t.Fatalf("settings defaults %+v differ from agent.DefaultConfig %+v", d, a)
	}
}

func TestLogDirResolves(t *testing.T) {
	for _, c := range []struct {
		db, dir, want string
		on            bool
	}{
		{"/data/ganglion.db", "", "/data/logs", true},
		{"ganglion.db", "", "logs", true},
		{"/data/ganglion.db", "off", "", false},
		{"/data/ganglion.db", "/var/log/ganglion", "/var/log/ganglion", true},
	} {
		f := settings.Default()
		f.Log.Dir = c.dir
		if dir, on := f.LogDir(c.db); dir != c.want || on != c.on {
			t.Errorf("LogDir(%q) with dir %q = %q, %v; want %q, %v", c.db, c.dir, dir, on, c.want, c.on)
		}
	}
}

// A skill script never gets a variable that holds one of the node's
// secrets, whatever the file names it (R-RTR-5, ADR-0042).
func TestSecretNamesListsEveryReference(t *testing.T) {
	f, err := settings.Parse([]byte(`telegram: {token: env:BOT}
agent: {search: {brave_key: env:SEARCH}}
models:
  providers:
    - {name: a, base_url: https://a.example/v1, key: env:A_KEY}
    - {name: l, base_url: http://localhost:1/v1}
    - {name: b, base_url: https://b.example/v1, key: env:SHARED}
  models: [{provider: a, name: m}]
  purposes: {chat: [{model: m}]}
`))
	if err != nil {
		t.Fatal(err)
	}
	got := f.SecretNames()
	slices.Sort(got)
	if want := []string{"A_KEY", "BOT", "SEARCH", "SHARED"}; !slices.Equal(got, want) {
		t.Fatalf("SecretNames = %q, want %q", got, want)
	}
	got = settings.Default().SecretNames()
	slices.Sort(got)
	if want := []string{"BRAVE_API_KEY", "TELEGRAM_BOT_TOKEN"}; !slices.Equal(got, want) {
		t.Fatalf("default SecretNames = %q, want %q", got, want)
	}
}
