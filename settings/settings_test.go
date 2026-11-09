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
