package jobs

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// maxTitleRunes cuts a long title, so a card always fits one message.
const maxTitleRunes = 200

// icons are the running card's marks by kind; a queued card and an ended
// one have theirs in their texts.
var icons = map[Kind]string{KindBuild: "🔨", KindCommand: "⚙️"}

// kindWords are the kinds as a person reads them.
var kindWords = map[Kind]texts.Key{KindBuild: texts.JobKindBuild, KindCommand: texts.JobKindCommand}

// endTexts are the cards of the jobs that ended.
var endTexts = map[State]texts.Key{StateDone: texts.JobCardDone, StateFailed: texts.JobCardFailed, StateInterrupted: texts.JobCardInterrupted}

// Elapsed is d as a card and the note show it: "0:45", "1:32", "1:02:03";
// cut to the second, and 0:00 for less than nothing.
func Elapsed(d time.Duration) string {
	s := int(max(d, 0) / time.Second)
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

// render is j's card at now in sp's language: the node's fixed words, the
// job's title as written and its count, never anything the job printed
// (ADR-0069).
func render(sp texts.Speaker, j Job, now time.Time) string {
	v := texts.Vars{"title": texts.Escape(text.Truncate(j.Title, maxTitleRunes)), "kind": sp.Text(kindWords[j.Kind], nil)}
	switch j.State {
	case StateQueued:
		return sp.Text(texts.JobCardQueued, v)
	case StateRunning:
		v["icon"], v["elapsed"] = icons[j.Kind], Elapsed(now.Sub(j.Started))
		line := sp.Text(texts.JobCardRunning, v)
		if j.Count > 0 {
			line += " · " + sp.Text(texts.JobCardCount, texts.Vars{"n": strconv.Itoa(j.Count)})
		}
		return line
	case StateDone, StateFailed, StateInterrupted:
		v["elapsed"] = Elapsed(j.Ended.Sub(j.Started))
		return sp.Text(endTexts[j.State], v)
	}
	return ""
}

// Note is what the model reads of a conversation's open jobs at the start of
// a turn, not stored in the history (ruling 15): "[jobs] running: build
// battery-alerts 0:45; command du -sh /home 1:10", then the queued ones; ""
// when none.
func Note(js []Job, now time.Time) string {
	var running, queued []string
	for _, j := range js {
		switch j.State {
		case StateRunning:
			running = append(running, string(j.Kind)+" "+j.Title+" "+Elapsed(now.Sub(j.Started)))
		case StateQueued:
			queued = append(queued, string(j.Kind)+" "+j.Title)
		case StateDone, StateFailed, StateInterrupted:
		}
	}
	var parts []string
	if len(running) > 0 {
		parts = append(parts, "running: "+strings.Join(running, "; "))
	}
	if len(queued) > 0 {
		parts = append(parts, "queued: "+strings.Join(queued, "; "))
	}
	if len(parts) == 0 {
		return ""
	}
	return "[jobs] " + strings.Join(parts, "; ")
}

// statusLines are the running jobs' lines of /status by kind.
var statusLines = map[Kind]texts.Key{KindBuild: texts.StatusJobBuild, KindCommand: texts.StatusJobCommand}

// Name implements status.Source and node.Component.
func (*Registry) Name() string { return "jobs" }

// Status implements status.Source: one line per open job of the node, in the
// "Background jobs" section (R-OBS-11).
func (r *Registry) Status(_ context.Context, s status.Scope) ([]status.Check, error) {
	var out []status.Check
	for _, j := range r.All() {
		c := status.Check{Section: status.SectionJobs, State: status.OK, Line: texts.StatusJobQueued,
			Vars: texts.Vars{"title": text.Truncate(j.Title, maxTitleRunes)}}
		if j.State == StateRunning {
			c.Line, c.Spans = statusLines[j.Kind], map[string]time.Duration{"elapsed": s.Now.Sub(j.Started)}
		}
		out = append(out, c)
	}
	return out, nil
}
