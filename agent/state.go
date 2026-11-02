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
)

// StateVersion is the version of State this code writes.
const StateVersion = 1

// State is a conversation's state: the current session's summary and the
// messages kept verbatim.
type State struct {
	V        int
	Session  string // id of the event that opened the session
	Started  time.Time
	Last     time.Time // the last turn
	Summary  string
	Messages []provider.Message
}

type wireState struct {
	V        int                `json:"v"`
	Session  string             `json:"session,omitempty"`
	Started  int64              `json:"started_ms,omitempty"`
	Last     int64              `json:"last_ms,omitempty"`
	Summary  string             `json:"summary"`
	Messages []provider.Message `json:"messages"`
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
	return json.Marshal(wireState{V: StateVersion, Session: s.Session, Started: ms(s.Started), Last: ms(s.Last), Summary: s.Summary, Messages: msgs})
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
	if w.V != StateVersion {
		return State{}, fmt.Errorf("agent: state version %d, this build reads %d", w.V, StateVersion)
	}
	return State{V: w.V, Session: w.Session, Started: fromMS(w.Started), Last: fromMS(w.Last), Summary: w.Summary, Messages: w.Messages}, nil
}

// tokens estimates the size of the state as runes/4.
func (s State) tokens() int {
	n := len([]rune(s.Summary))
	for _, m := range s.Messages {
		n += len([]rune(m.Content))
		for _, tc := range m.ToolCalls {
			n += len([]rune(tc.Name)) + len(tc.Args)
		}
	}
	return n / 4
}
