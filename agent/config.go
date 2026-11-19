package agent

import "time"

// Config tunes the agent. Start from DefaultConfig and override fields;
// a zero daily limit or SessionIdle turns that feature off.
type Config struct {
	MaxSteps      int           // model calls per turn; on-demand calls do not spend it (ADR-0100)
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
	// Review runs the memory review of a quiet private chat (R-MEM-8);
	// ReviewQuiet is how long the chat is silent first.
	Review      bool
	ReviewQuiet time.Duration
}

// DefaultConfig returns the agent's defaults.
func DefaultConfig() Config {
	return Config{
		MaxSteps:      20,
		TurnTimeout:   5 * time.Minute,
		CallTimeout:   3 * time.Minute,
		HistoryTokens: 6000,
		KeepRecent:    12,
		DailyTurns:    300,
		DailyTokens:   1_000_000,
		SessionIdle:   6 * time.Hour,
		Location:      time.UTC,
	}
}
