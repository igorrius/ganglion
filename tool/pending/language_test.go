package pending_test

import (
	"context"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/texts"
)

func TestButtonsCarrySymbolsFromCode(t *testing.T) {
	nonsense, err := texts.New(texts.WithPacks(func(l texts.Lang, _ texts.Key) (string, bool) {
		return "nonsense", l == "pl"
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		speak         texts.Speaker
		approve, deny string
	}{
		{texts.Speaker{}, "✅ Approve", "❌ Deny"},
		{texts.Speaker{Lang: "uk"}, "✅ Дозволити", "❌ Відхилити"},
		{texts.Speaker{Lang: "pl", Cat: nonsense}, "✅ nonsense", "❌ nonsense"},
	} {
		f := newFixture(t)
		c := call(1)
		c.Speak = tc.speak
		ctx := context.Background()
		id, err := f.s.Ask(ctx, c, "Run it?")
		if err != nil {
			t.Fatal(err)
		}
		q, err := f.s.Outgoing(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if q.Buttons[0].Label != tc.approve || q.Buttons[1].Label != tc.deny {
			t.Errorf("%s: %+v", tc.speak.Lang, q.Buttons)
		}
	}
}

func TestRerunKeepsTheSameQuestion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	c := call(1)
	id, err := f.s.Ask(ctx, c, "Run it?")
	if err != nil {
		t.Fatal(err)
	}
	c.Speak = texts.Speaker{Lang: "uk"}
	again, err := f.s.Ask(ctx, c, "Виконати?")
	if err != nil {
		t.Fatal(err)
	}
	q, err := f.s.Outgoing(ctx, again)
	if err != nil {
		t.Fatal(err)
	}
	if again != id || q.Text != "Run it?" || !strings.HasSuffix(q.Buttons[0].Label, "Approve") {
		t.Fatalf("id %q, want %q; %+v", again, id, q)
	}
}
