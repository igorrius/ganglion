package agent

import "time"

// Config tunes the agent. Start from DefaultConfig and override fields;
// a zero daily limit or SessionIdle turns that feature off.
type Config struct {
	MaxSteps      int           // model calls per turn
	TurnTimeout   time.Duration // the whole turn
	CallTimeout   time.Duration // one model call
	HistoryTokens int           // compact above this estimate
	KeepRecent    int           // messages kept verbatim at compaction
	DailyTurns    int           // per principal; 0 = off
	DailyTokens   int           // per principal; 0 = off
	SessionIdle   time.Duration // a new session after this much silence; 0 = off
	Location      *time.Location
	// Home is the node user's home, the folder of a host command without
	// dir: an approved plan's command matches a call with "" read as home
	// (tool.Command.SameAs), and a task run's commands are shown with it.
	Home string
}

// DefaultConfig returns the agent's defaults.
func DefaultConfig() Config {
	return Config{
		MaxSteps:      8,
		TurnTimeout:   2 * time.Minute,
		CallTimeout:   60 * time.Second,
		HistoryTokens: 6000,
		KeepRecent:    12,
		DailyTurns:    300,
		DailyTokens:   1_000_000,
		SessionIdle:   6 * time.Hour,
		Location:      time.UTC,
	}
}
