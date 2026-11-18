// Package agent handles a conversation turn with a model: it keeps the
// conversation's history as its state (ADR-0020), routes model calls with
// fallback, runs tools up to a step limit, compacts long history into a
// summary and enforces daily limits (docs/spec.md, 7.2).
package agent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/skills"
)

// StateVersion is the version of State this code writes. Version 2 added
// Tainted and Pending; version 1 is still read.
const StateVersion = 2

// firstStateVersion is the oldest version DecodeState reads.
const firstStateVersion = 1

// State is a conversation's state: the current session's summary and the
// messages kept verbatim.
type State struct {
	V        int
	Session  string // id of the event that opened the session
	Started  time.Time
	Last     time.Time // the last turn
	Summary  string
	Messages []provider.Message
	// Tainted is set once external content entered the session; it lasts
	// until the session ends (design section 4, rule 1).
	Tainted bool
	// LocalOnly is set once a local-only skill was read: its data is in the
	// history, so the session stays on local models until it ends (ADR-0042).
	LocalOnly bool
	// Pending are the ids of calls awaiting the owner's confirmation.
	Pending []string
	// Commands are the keys of the session's background jobs whose end is
	// still to be told: host commands (R-CMD-4) and module builds (R-RUN-9).
	Commands []string
	// Reviewed is how many leading Messages the memory review has read
	// (R-MEM-8); compaction lowers it by what it folds.
	Reviewed int
	// Tools are the on-demand tools turned on in this session; they stay
	// until the session ends (R-MCP-4a).
	Tools []string
}

type wireState struct {
	V        int                `json:"v"`
	Session  string             `json:"session,omitempty"`
	Started  int64              `json:"started_ms,omitempty"`
	Last     int64              `json:"last_ms,omitempty"`
	Summary  string             `json:"summary"`
	Messages []provider.Message `json:"messages"`
	Tainted  bool               `json:"tainted,omitempty"`
	Local    bool               `json:"local_only,omitempty"`
	Pending  []string           `json:"pending,omitempty"`
	Commands []string           `json:"commands,omitempty"`
	Reviewed int                `json:"reviewed,omitempty"`
	Tools    []string           `json:"tools,omitempty"`
}

// privacy is the routing privacy the session's history demands.
func (s State) privacy() string {
	if s.LocalOnly {
		return skills.PrivacyLocalOnly
	}
	return ""
}

func ms(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func fromMS(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

// MarshalJSON writes the versioned wire form.
func (s State) MarshalJSON() ([]byte, error) {
	msgs := s.Messages
	if msgs == nil {
		msgs = []provider.Message{}
	}
	return json.Marshal(wireState{V: StateVersion, Session: s.Session, Started: ms(s.Started), Last: ms(s.Last), Summary: s.Summary, Messages: msgs, Tainted: s.Tainted, Local: s.LocalOnly, Pending: s.Pending,
		Commands: s.Commands, Reviewed: s.Reviewed, Tools: s.Tools})
}

// DecodeState reads a stored state; nil is an empty state.
func DecodeState(raw json.RawMessage) (State, error) {
	if len(raw) == 0 {
		return State{V: StateVersion}, nil
	}
	var w wireState
	if err := json.Unmarshal(raw, &w); err != nil {
		return State{}, fmt.Errorf("agent: state: %w", err)
	}
	if w.V < firstStateVersion || w.V > StateVersion {
		return State{}, fmt.Errorf("agent: state version %d, this build reads %d to %d", w.V, firstStateVersion, StateVersion)
	}
	return State{V: StateVersion, Tainted: w.Tainted, LocalOnly: w.Local, Pending: w.Pending, Commands: w.Commands, Session: w.Session, Started: fromMS(w.Started), Last: fromMS(w.Last), Summary: w.Summary, Messages: w.Messages, Reviewed: w.Reviewed, Tools: w.Tools}, nil
}

// tokens estimates the size of the state as runes/4.
func (s State) tokens() int {
	return (len([]rune(s.Summary)) + runesOf(s.Messages)) / 4
}

// estimateTokens is the size of messages as runes/4.
func estimateTokens(msgs []provider.Message) int { return runesOf(msgs) / 4 }

// runesOf counts the runes of messages, their tool calls included.
func runesOf(msgs []provider.Message) int {
	n := 0
	for _, m := range msgs {
		n += len([]rune(m.Content))
		for _, tc := range m.ToolCalls {
			n += len([]rune(tc.Name)) + len(tc.Args)
		}
	}
	return n
}
