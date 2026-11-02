package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider/openai"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/router"
)

func TestRunKeepsHistoryAndReports(t *testing.T) {
	fake := providertest.NewServer(t, "k")
	fake.Reply("Барсик")
	fake.Fail(500, 0)
	m := router.Model{Provider: "or", Name: "m", Caps: router.ModelCaps{PriceInMTok: 1, PriceOutMTok: 2}}
	convs := []Conversation{{Name: "cat", Turns: []string{"Кот Барсик.", "Как зовут кота?"}}}
	res := run(context.Background(), openai.New("or", fake.URL(), "k"), m, convs, time.Second)
	if res.Answers[0][0].Text != "Барсик" || res.Answers[0][1].Err == "" {
		t.Fatalf("answers %+v", res.Answers)
	}
	if got := fake.Requests()[1].Messages; len(got) != 4 || got[3]["content"] != "Igor: Как зовут кота?" {
		t.Fatalf("second request did not carry the history: %v", got)
	}
	var b bytes.Buffer
	if err := report(&b, convs, []Result{res}, time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	out := b.String()
	for _, want := range []string{"| or/m | 1/2 |", "**Igor:** Как зовут кота?", "**error:**", "> Барсик"} {
		if !strings.Contains(out, want) {
			t.Errorf("report lacks %q:\n%s", want, out)
		}
	}
}
