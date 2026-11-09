package settings

import "fmt"

// retired maps each variable that held a setting before M4 to the key that
// replaced it (R-CFG-5). Variables not listed here are not ganglion's
// settings: the config path, the database path, secrets, OpenTelemetry's
// own, the bridge's per-run token and the test tools' variables.
var retired = []struct{ env, key string }{
	{"GANGLION_LOG_LEVEL", "log.level"},
	{"GANGLION_ADMIN", "telegram.admin"},
	{"TELEGRAM_API_URL", "telegram.api_url"},
	{"GANGLION_TELEGRAM_REPLY", "telegram.reply"},
	{"GANGLION_TELEGRAM_RICH", "telegram.rich"},
	{"GANGLION_REACTION_AFTER", "telegram.reactions.after"},
	{"GANGLION_REACTION_DWELL", "telegram.reactions.dwell"},
	{"GANGLION_METRICS_ADDR", "metrics.addr"},
	{"GANGLION_LOG_DIR", "log.dir"},
	{"GANGLION_MODELS_FILE", "models"},
	{"GANGLION_LANGUAGES", "agent.languages"},
	{"GANGLION_WORKSPACE", "agent.workspace"},
	{"GANGLION_SANDBOX_DENY", "sandbox.deny"},
	{"GANGLION_SANDBOX_HIDE_NAMES", "sandbox.hide_names"},
	{"GANGLION_DAILY_BUDGET_USD", "agent.limits.daily_budget_usd"},
	{"GANGLION_DAILY_TURNS", "agent.limits.daily_turns"},
	{"GANGLION_DAILY_TOKENS", "agent.limits.daily_tokens"},
	{"GANGLION_SESSION_IDLE", "agent.session_idle"},
	{"GANGLION_TZ", "node.tz"},
	{"GANGLION_SEARCH", "agent.search.mode"},
}

// Retired returns an error for the first retired variable that is set.
func Retired(getenv func(string) string) error {
	for _, r := range retired {
		if getenv(r.env) != "" {
			return fmt.Errorf("%s is no longer read: set %s in %s", r.env, r.key, FileName)
		}
	}
	return nil
}
