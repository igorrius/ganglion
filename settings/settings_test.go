package settings_test

import (
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
