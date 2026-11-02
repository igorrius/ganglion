package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/memory"
	"github.com/igorrius/ganglion/provider"
)

// Limits of a search of the archive.
const (
	// searchSessions is how many of the newest archived sessions a search
	// reads; older ones are not searched.
	searchSessions = 500
	// snippetRunes is how much text around a match a hit carries.
	snippetRunes = 240
)

var _ memory.Episodes = (*Archive)(nil)

// Search returns at most limit archived sessions of the conversations
// convs whose summary or messages hold q, case insensitive, newest first,
// each with the text around its first match (memory.Episodes). Only the
// newest searchSessions sessions of those conversations are read; no
// convs finds nothing.
func (a *Archive) Search(ctx context.Context, q string, convs []string, limit int) ([]memory.Episode, error) {
	needle := strings.ToLower(strings.TrimSpace(q))
	if needle == "" || limit <= 0 || len(convs) == 0 {
		return nil, nil
	}
	args := make([]any, 0, len(convs)+1)
	for _, c := range convs {
		args = append(args, c)
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(convs)), ", ")
	rows, err := a.db.QueryContext(ctx, `SELECT conv, data FROM sessions WHERE conv IN (`+marks+`)
		ORDER BY ended_ms DESC, rowid DESC LIMIT ?`, append(args, searchSessions)...)
	if err != nil {
		return nil, fmt.Errorf("agent: search sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []memory.Episode
	for rows.Next() && len(out) < limit {
		var conv, data string
		if err := rows.Scan(&conv, &data); err != nil {
			return nil, fmt.Errorf("agent: search sessions: %w", err)
		}
		st, err := DecodeState([]byte(data))
		if err != nil {
			continue // an unreadable session has nothing to find
		}
		if text, ok := match(st, needle); ok {
			out = append(out, memory.Episode{Conv: conv, Session: st.Session, Ended: st.Last, Tainted: st.Tainted, Text: text})
		}
	}
	return out, rows.Err()
}

// match returns the text around the first place needle (lower case)
// occurs in the session's summary or messages.
func match(st State, needle string) (string, bool) {
	texts := []string{st.Summary}
	for _, m := range st.Messages {
		if m.Role != provider.RoleSystem {
			texts = append(texts, m.Content)
		}
	}
	for _, t := range texts {
		if s, ok := snippet(t, needle); ok {
			return s, true
		}
	}
	return "", false
}

// snippet is about snippetRunes of text around the first match of
// needle, compared in lower case rune by rune, so the offsets hold for
// any script.
func snippet(text, needle string) (string, bool) {
	rs, lower := []rune(text), []rune(strings.ToLower(text))
	n := []rune(needle)
	if len(lower) != len(rs) {
		lower = rs // a letter whose lower case is longer: compare as written
	}
	at := indexRunes(lower, n)
	if at < 0 {
		return "", false
	}
	start := max(0, at-(snippetRunes-len(n))/2)
	end := min(len(rs), start+snippetRunes)
	s := strings.TrimSpace(string(rs[start:end]))
	if start > 0 {
		s = "…" + s
	}
	if end < len(rs) {
		s += "…"
	}
	return s, true
}

// indexRunes is the first index of sub in s, or -1.
func indexRunes(s, sub []rune) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if slices.Equal(s[i:i+len(sub)], sub) {
			return i
		}
	}
	return -1
}
