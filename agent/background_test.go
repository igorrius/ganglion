package agent

import (
	"context"
	"strings"
	"testing"
	"time"

	bg "github.com/igorrius/ganglion/jobs"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// running lists fixed background jobs by conversation.
type running map[string][]bg.Job

func (r running) Running(conv string) []bg.Job { return r[conv] }

// Every turn tells the model what runs in the background of its
// conversation, in a note that is not stored (R-LIVE-9, ruling 15).
func TestTheJobsNoteReachesTheModelAndNotTheHistory(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.deps.Background = running{
		"telegram:42": {{Key: "b:1", Kind: bg.KindBuild, Conv: "telegram:42", Title: "battery-alerts", State: bg.StateRunning, Started: t0.Add(-45 * time.Second)}},
		"telegram:7":  {{Key: "c:1", Kind: bg.KindCommand, Conv: "telegram:7", Title: "ls", State: bg.StateRunning, Started: t0}},
	}
	e.a = New(overDefaults(Config{}), e.deps)
	e.turn(t, message(owner, "Igor", "how is it going?"))
	got := lastUserRaw(e.smart.requests()[0])
	if want := "Igor: how is it going?\n[now: 2026-11-03 10:00 Tue, UTC]\n[jobs] running: build battery-alerts 0:45"; got != want {
		t.Errorf("the model saw %q, want %q", got, want)
	}
	for _, m := range e.state(t).Messages {
		if strings.Contains(m.Content, "[jobs]") {
			t.Errorf("history keeps the jobs note: %q", m.Content)
		}
	}
}

// With nothing running, a turn carries no jobs line.
func TestNoJobsNoNote(t *testing.T) {
	e := newEnv(t, Config{}, nil)
	e.turn(t, message(owner, "Igor", "hi"))
	if got := lastUserRaw(e.smart.requests()[0]); strings.Contains(got, "[jobs]") {
		t.Errorf("the model saw %q", got)
	}
}

// /status has a "Background jobs" section with the registry's lines (R-OBS-11).
func TestStatusListsBackgroundJobs(t *testing.T) {
	store, err := bg.NewStore(t.Context(), newEnv(t, Config{}, nil).db)
	if err != nil {
		t.Fatal(err)
	}
	reg := bg.New(bg.Deps{Store: store, Outbox: &enqueued{}, Texts: texts.Shipped(), Now: func() time.Time { return t0.Add(-80 * time.Second) }})
	if err := reg.Start(context.Background(), bg.Job{Key: "b:1", Kind: bg.KindBuild, Conv: "telegram:42", Transport: "telegram",
		Principal: owner, Title: "battery-alerts"}); err != nil {
		t.Fatal(err)
	}
	e := statusEnv(t, Config{}, reg)
	reply := statusReply(t, e)
	for _, want := range []string{"## Background jobs", "✅ Build battery-alerts: 1 min"} {
		if !strings.Contains(reply, want) {
			t.Errorf("reply lacks %q:\n%s", want, reply)
		}
	}
	if strings.Index(reply, "## Delivery") > strings.Index(reply, "## Background jobs") {
		t.Errorf("sections out of order:\n%s", reply)
	}
	var _ status.Source = reg
}
