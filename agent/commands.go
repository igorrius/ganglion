package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/turns"
)

// command answers a chat command without a model; arg is the text after
// the command word.
type command func(ctx context.Context, t *turn, arg string) error

const (
	replyNewSession   = "New session started."
	replyNoSessions   = "No earlier sessions."
	replyNoSuchResume = "No such session; see /sessions."
	sessionsShown     = 5
)

// parseCommand returns the command word of text ("/new@bot" → "/new") and
// the rest, or "" when text is not a command.
func parseCommand(text string) (string, string) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "", ""
	}
	word, rest, _ := strings.Cut(text, " ")
	word, _, _ = strings.Cut(word, "@")
	return strings.ToLower(word), strings.TrimSpace(rest)
}

// command runs the message's command from the table; other messages go
// on to the model, unknown commands included. A scheduled task's
// instruction is never a command.
func (a *Agent) command(ctx context.Context, t *turn) (bool, error) {
	if t.grant != nil {
		return false, nil
	}
	name, arg := parseCommand(t.msg.Text)
	run, ok := a.commands[name]
	if !ok {
		return false, nil
	}
	t.record.Outcome = turns.OutcomeCommand
	return true, run(ctx, t, arg)
}

// newSession archives the session; the next real message opens a new one
// and sets its id and start.
func (a *Agent) newSession(ctx context.Context, t *turn, _ string) error {
	if err := a.archive(ctx, t.event.Conv, t.state, "new"); err != nil {
		return err
	}
	t.state = State{V: StateVersion}
	t.reply(replyNewSession)
	return nil
}

// listSessions shows the last archived sessions, newest first.
func (a *Agent) listSessions(ctx context.Context, t *turn, _ string) error {
	list, err := a.d.Archive.List(ctx, t.event.Conv, sessionsShown, t.state.Session)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		t.reply(replyNoSessions)
		return nil
	}
	lines := make([]string, len(list))
	for i, s := range list {
		lines[i] = fmt.Sprintf("%d. %s · %d turns · %s", i+1, a.formatSpan(s), s.Turns, s.Title)
	}
	t.reply(strings.Join(lines, "\n"))
	return nil
}

// resumeSession brings back session number arg of /sessions, archiving
// the current one. Calls it was waiting for that are answered or expired
// since are dropped from its pending list.
func (a *Agent) resumeSession(ctx context.Context, t *turn, arg string) error {
	list, err := a.d.Archive.List(ctx, t.event.Conv, sessionsShown, t.state.Session)
	if err != nil {
		return err
	}
	n, err := strconv.Atoi(arg)
	if err != nil || n < 1 || n > len(list) {
		t.reply(replyNoSuchResume)
		return nil
	}
	chosen, err := a.d.Archive.Get(ctx, t.event.Conv, list[n-1].Session)
	if err != nil {
		return err
	}
	if t.state.Session != chosen.Session {
		if err := a.archive(ctx, t.event.Conv, t.state, "resume"); err != nil {
			return err
		}
	}
	if chosen.Pending, err = a.d.Pending.Waiting(ctx, chosen.Pending); err != nil {
		return err
	}
	chosen.Last = t.now
	t.state = chosen
	t.reply("Resumed: " + list[n-1].Title)
	return nil
}

func (a *Agent) archive(ctx context.Context, conv string, st State, by string) error {
	if len(st.Messages) == 0 {
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
