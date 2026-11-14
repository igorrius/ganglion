package agent

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/memory"
)

// topicsView is a MemoryView over fixed topics.
type topicsView []memory.Topic

func (v topicsView) Topics() ([]memory.Topic, error) { return v, nil }

func (v topicsView) Topic(slug string) (memory.Topic, error) {
	for _, t := range v {
		if t.Slug == slug {
			return t, nil
		}
	}
	return memory.Topic{}, fmt.Errorf("%w: %s", memory.ErrNotFound, slug)
}

var memoryTopics = topicsView{
	{Slug: "car", Title: "Car", Summary: "The family car", Facts: []memory.Fact{{ID: "f1", Text: "The car is blue."}}},
	{Slug: "inverter", Title: "Inverter", Summary: "The Acme inverter", Facts: []memory.Fact{
		{ID: "f1", Text: "Negative batt_w is the battery charging.", Date: time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)},
		{ID: "f2", Text: "The battery is 10 kWh.", Date: time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC), Tainted: true},
	}},
}

func memoryCmdEnv(t *testing.T, v MemoryView) *env {
	t.Helper()
	e := newEnv(t, Config{}, nil)
	e.deps.Memory = v
	e.deps.Private = privateOf
	e.a = New(overDefaults(Config{}), e.deps)
	return e
}

// /memory lists the topics and shows one, with no model and no note in
// the history (R-MEM-9).
func TestMemoryCommandListsAndShows(t *testing.T) {
	e := memoryCmdEnv(t, memoryTopics)
	e.smart.push(say("Hello."))
	e.turn(t, message(owner, "Igor", "hi"))
	calls, history := len(e.smart.requests()), len(e.state(t).Messages)
	out := e.turn(t, message(owner, "Igor", "/memory"))
	want := "What I remember, by topic:\n- Car (car), facts: 1 — The family car\n- Inverter (inverter), facts: 2 — The Acme inverter"
	if len(out) != 1 || out[0].Text != want {
		t.Fatalf("list %+v", out)
	}
	out = e.turn(t, message(owner, "Igor", "/memory inverter"))
	want = "Inverter (inverter) — The Acme inverter\n- [f1] Negative batt\\_w is the battery charging. (2026-11-11)\n" +
		"- [f2] The battery is 10 kWh. (2026-11-12, from outside content)"
	if len(out) != 1 || out[0].Text != want {
		t.Fatalf("topic %q", out[0].Text)
	}
	if out := e.turn(t, message(owner, "Igor", "/memory boat")); len(out) != 1 || out[0].Text != "No topic boat. /memory lists them." {
		t.Fatalf("unknown %+v", out)
	}
	if len(e.smart.requests()) != calls || len(e.state(t).Messages) != history {
		t.Fatal("/memory reached the model or the history")
	}
}

func TestMemoryCommandEmptyAndRefused(t *testing.T) {
	e := memoryCmdEnv(t, NoMemory{})
	if out := e.turn(t, message(owner, "Igor", "/memory")); len(out) != 1 || out[0].Text != "I remember nothing yet." {
		t.Fatalf("empty %+v", out)
	}
	e = memoryCmdEnv(t, memoryTopics)
	ev := message(owner, "Igor", "/memory")
	ev.Conv = "telegram:-100500" // a group
	if out := e.turn(t, ev); len(out) != 1 || !strings.Contains(out[0].Text, "private chat") || strings.Contains(out[0].Text, "Car") {
		t.Fatalf("in a group %+v", out)
	}
	e.admin = false
	if out := e.turn(t, message("telegram:user:7", "Olena", "/memory")); len(out) != 1 || out[0].Text != replyTasksNotAvailable {
		t.Fatalf("a family member got %+v", out)
	}
}

func TestMemoryCommandHasHints(t *testing.T) {
	e := memoryCmdEnv(t, NoMemory{})
	for _, c := range e.a.Commands() {
		if c.Name == "memory" && len(c.Description) == 3 {
			return
		}
	}
	t.Fatal("/memory is not listed with hints in three languages")
}
