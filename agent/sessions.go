package agent

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Archive keeps the ended sessions of every conversation (R-AGT-6).
type Archive struct{ db *sql.DB }

// SessionInfo is one line of /sessions.
type SessionInfo struct {
	Session        string
	Started, Ended time.Time
	Turns          int
	Title          string // the first user message, at most 60 runes
}

// NewArchive migrates the "sessions" component.
func NewArchive(ctx context.Context, db *sql.DB) (*Archive, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "sessions", sub); err != nil {
		return nil, err
	}
	return &Archive{db: db}, nil
}

func title(s State) string {
	for _, m := range s.Messages {
		if m.Role == "user" {
			return truncate(m.Content, 60)
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// Put archives s, ended at s.Last. It is keyed by (conv, session), so a
// turn that reruns after a crash writes the same row again.
func (a *Archive) Put(ctx context.Context, conv string, s State, endedBy string) error {
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	turns := 0
	for _, m := range s.Messages {
		if m.Role == "user" {
			turns++
		}
	}
	_, err = a.db.ExecContext(ctx, `INSERT OR REPLACE INTO sessions
		(conv, session, started_ms, ended_ms, ended_by, turns, title, data) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		conv, s.Session, ms(s.Started), ms(s.Last), endedBy, turns, title(s), string(data))
	if err != nil {
		return fmt.Errorf("agent: archive session %s: %w", s.Session, err)
	}
	return nil
}

// List returns the last n archived sessions of conv, newest first,
// leaving out the session except (the current one: it may sit in the
// archive as an older copy after a /resume).
func (a *Archive) List(ctx context.Context, conv string, n int, except string) ([]SessionInfo, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT session, started_ms, ended_ms, turns, title FROM sessions
		WHERE conv = ? AND session != ? ORDER BY ended_ms DESC, rowid DESC LIMIT ?`, conv, except, n)
	if err != nil {
		return nil, fmt.Errorf("agent: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []SessionInfo
	for rows.Next() {
		var s SessionInfo
		var started, ended int64
		if err := rows.Scan(&s.Session, &started, &ended, &s.Turns, &s.Title); err != nil {
			return nil, err
		}
		s.Started, s.Ended = fromMS(started), fromMS(ended)
		out = append(out, s)
	}
	return out, rows.Err()
}

// Get returns an archived session.
func (a *Archive) Get(ctx context.Context, conv, session string) (State, error) {
	var data string
	if err := a.db.QueryRowContext(ctx, `SELECT data FROM sessions WHERE conv = ? AND session = ?`, conv, session).Scan(&data); err != nil {
		return State{}, fmt.Errorf("agent: get session %s: %w", session, err)
	}
	return DecodeState(json.RawMessage(data))
}

// command returns the command word of text ("/new@bot" → "/new") and the
// rest, or "" when text is not a command.
func command(text string) (string, string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	word, rest, _ := strings.Cut(text, " ")
	word, _, _ = strings.Cut(word, "@")
	return strings.ToLower(word), strings.TrimSpace(rest)
}

const (
	replyNewSession   = "New session started."
	replyNoSessions   = "No earlier sessions."
	replyNoSuchResume = "No such session; see /sessions."
	sessionsShown     = 5
)

func (a *Agent) archive(ctx context.Context, conv string, st State, by string) error {
	if a.d.Archive == nil || len(st.Messages) == 0 {
		return nil
	}
	return a.d.Archive.Put(ctx, conv, st, by)
}

func (a *Agent) formatSpan(s SessionInfo) string {
	start, end := s.Started.In(a.cfg.Location), s.Ended.In(a.cfg.Location)
	if start.YearDay() == end.YearDay() && start.Year() == end.Year() {
		return start.Format("2 Jan 15:04") + "–" + end.Format("15:04")
	}
	return start.Format("2 Jan 15:04") + "–" + end.Format("2 Jan 15:04")
}

// sessions starts a new session after a long silence and handles the
// session commands. handled means the event is answered without a model.
func (a *Agent) sessions(ctx context.Context, e event.Event, st State, m event.Message, now time.Time, tr *turnRecord,
	reply func(string) event.Outgoing) (State, []event.Outgoing, bool, error) {
	fresh := State{V: StateVersion, Session: e.ID, Started: now, Last: now}
	if a.cfg.SessionIdle > 0 && len(st.Messages) > 0 && !st.Last.IsZero() && now.Sub(st.Last) > a.cfg.SessionIdle {
		if err := a.archive(ctx, e.Conv, st, "idle"); err != nil {
			return st, nil, false, err
		}
		a.log.Info("agent: new session after silence", "conv", e.Conv, "silent_for", now.Sub(st.Last).Round(time.Minute))
		st = fresh
	}

	cmd, arg := command(m.Text)
	switch cmd {
	case "/new", "/reset":
		if err := a.archive(ctx, e.Conv, st, "new"); err != nil {
			return st, nil, false, err
		}
		tr.Outcome = "command"
		// The next real message opens the session and sets its id and start.
		return State{V: StateVersion}, []event.Outgoing{reply(replyNewSession)}, true, nil
	case "/sessions", "/resume":
		tr.Outcome = "command"
		if a.d.Archive == nil {
			return st, []event.Outgoing{reply(replyNoSessions)}, true, nil
		}
		list, err := a.d.Archive.List(ctx, e.Conv, sessionsShown, st.Session)
		if err != nil {
			return st, nil, false, err
		}
		if cmd == "/sessions" {
			if len(list) == 0 {
				return st, []event.Outgoing{reply(replyNoSessions)}, true, nil
			}
			lines := make([]string, len(list))
			for i, s := range list {
				lines[i] = fmt.Sprintf("%d. %s · %d turns · %s", i+1, a.formatSpan(s), s.Turns, s.Title)
			}
			return st, []event.Outgoing{reply(strings.Join(lines, "\n"))}, true, nil
		}
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > len(list) {
			return st, []event.Outgoing{reply(replyNoSuchResume)}, true, nil
		}
		chosen, err := a.d.Archive.Get(ctx, e.Conv, list[n-1].Session)
		if err != nil {
			return st, nil, false, err
		}
		if st.Session != chosen.Session {
			if err := a.archive(ctx, e.Conv, st, "resume"); err != nil {
				return st, nil, false, err
			}
		}
		chosen.Last = now
		return chosen, []event.Outgoing{reply("Resumed: " + list[n-1].Title)}, true, nil
	}
	return st, nil, false, nil
}
