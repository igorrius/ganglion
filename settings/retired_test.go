package settings_test

import (
	"testing"

	"github.com/igorrius/ganglion/settings"
)

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
		if err := settings.Retired(func(v string) string {
			if v == k {
				return "x"
			}
			return ""
		}); err == nil {
			t.Errorf("%s is not retired", k)
		}
	}
}
