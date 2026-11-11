package agent

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/internal/clock"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/turns"
)

const (
	replyNoTurn = "No turn in this conversation yet."
	// maxToolsShown bounds the list so /last stays one message.
	maxToolsShown = 30
	// maxErrorShown is the runes of a turn's error /last shows.
	maxErrorShown = 200
	// blockFence shows the summary in monospace, which lines the tools up and
	// keeps an error's text from being read as formatting.
	blockFence = "```"
)

// outcomeWords is how /last names a turn's outcome.
var outcomeWords = map[turns.Outcome]string{
	turns.OutcomeOK:         "ok",
	turns.OutcomeStepLimit:  "step limit",
	turns.OutcomeDailyLimit: "daily limit",
	turns.OutcomeNoModel:    "no model",
	turns.OutcomeCommand:    "command",
	turns.OutcomeError:      "error",
	turns.OutcomeReview:     "memory review",
}

// lastTurn shows an admin what the conversation's last turn did, with no model
// and no note in the history (R-OBS-10).
func (a *Agent) lastTurn(ctx context.Context, t *turn, _ string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	last, err := a.d.Turns.Last(ctx, t.event.Conv)
	if errors.Is(err, turns.ErrNoTurn) {
		t.reply(replyNoTurn)
		return nil
	}
	if err != nil {
		return fmt.Errorf("agent: /last: %w", err)
	}
	// The error can quote anything, so no fence may close early: every backtick goes.
	body := strings.ReplaceAll(showTurn(last, a.cfg.Location, t.now), "`", "'")
	t.reply(blockFence + "\n" + body + "\n" + blockFence)
	return nil
}

// showTurn is the turn in plain words. It names tools and never shows their
// arguments or results; an error is one short line with secrets removed.
func showTurn(t turns.Turn, loc *time.Location, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Last turn, %s · %s · %s\n", whenStarted(t.Started, loc, now),
		seconds(t.Finished.Sub(t.Started).Milliseconds()), outcomeWords[t.Outcome])
	if t.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", text.Truncate(strings.Join(strings.Fields(redact.String(t.Error)), " "), maxErrorShown))
	}
	if route := showRoute(t.Route); route != "" {
		fmt.Fprintf(&b, "Model: %s\n", route)
	}
	fmt.Fprintf(&b, "Steps: %d\n", t.Steps)
	writeTools(&b, t.Tools)
	if t.Joined > 0 {
		fmt.Fprintf(&b, "Joined: %s\n", plural(t.Joined, "message"))
	}
	fmt.Fprintf(&b, "Tokens: %s\n", showTokens(t))
	fmt.Fprintf(&b, "Cost: $%.4f", t.CostUSD)
	return b.String()
}

// whenStarted is the time in loc, with the date when it was not today.
func whenStarted(at time.Time, loc *time.Location, now time.Time) string {
	if clock.DayStart(at, loc).Equal(clock.DayStart(now, loc)) {
		return at.In(loc).Format("15:04")
	}
	return at.In(loc).Format("2 Jan 15:04")
}

func seconds(ms int64) string { return fmt.Sprintf("%.1f s", float64(ms)/1000) }

// showRoute says which models the turn used, in order. A run ended for an
// addition is no failure and does not show; a model used for several steps
// shows once.
func showRoute(route []turns.Attempt) string {
	route = slices.DeleteFunc(slices.Clone(route), func(a turns.Attempt) bool { return a.Error == turns.EndedForAddition })
	route = slices.Compact(route)
	parts := make([]string, 0, len(route))
	for _, a := range route {
		s := a.Model + " (" + a.Provider + ")"
		if a.Error != "" {
			s += " failed (" + a.Error + ")"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, ", then ")
}

// writeTools lists the turn's tool calls, their names lined up. A call a rerun
// brought back has lost its time (Ms 0 with a note of an answer) and shows none.
func writeTools(b *strings.Builder, tools []turns.ToolUse) {
	if len(tools) == 0 {
		return
	}
	shown := tools[:min(len(tools), maxToolsShown)]
	width := 0
	for _, u := range shown {
		width = max(width, utf8.RuneCountInString(u.Name))
	}
	b.WriteString("Tools:\n")
	for _, u := range shown {
		line := "  " + toolMark(u) + " " + u.Name + strings.Repeat(" ", width-utf8.RuneCountInString(u.Name))
		if u.Ms > 0 || u.Note == "" || u.Note == turns.NoteAsked {
			line += "  " + seconds(u.Ms)
		}
		if u.Note != "" {
			line += "  " + string(u.Note)
		}
		b.WriteString(line + "\n")
	}
	if more := len(tools) - len(shown); more > 0 {
		fmt.Fprintf(b, "  … and %d more\n", more)
	}
}

// toolMark is ✓ for a call that ran and worked, ✗ for one that failed, · for one
// that did not run.
func toolMark(u turns.ToolUse) string {
	switch {
	case u.Note == turns.NoteAsked || u.Note == turns.NoteDeclined || u.Note == turns.NoteExpired:
		return "·"
	case u.OK:
		return "✓"
	default:
		return "✗"
	}
}

func showTokens(t turns.Turn) string {
	in := thousands(t.InputTokens) + " in"
	if t.CacheReadTokens > 0 {
		in += " (" + thousands(t.CacheReadTokens) + " cached)"
	}
	return in + ", " + thousands(t.OutputTokens) + " out"
}

// thousands writes n with commas.
func thousands(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}
