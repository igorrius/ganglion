package telegram

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

func TestReadRich(t *testing.T) {
	bold := `{"type":"bold","text":"b"}`
	for _, tc := range []struct{ name, blocks, want string }{
		{"paragraph", `[{"type":"paragraph","text":"a"}]`, "a"},
		{"rich text list", `[{"type":"paragraph","text":["a ",` + bold + `]}]`, "a b"},
		{"nested rich text", `[{"type":"paragraph","text":{"type":"italic","text":` + bold + `}}]`, "b"},
		{"two blocks", `[{"type":"heading","text":"h","size":1},{"type":"paragraph","text":"p"}]`, "h\n\np"},
		{"pre", `[{"type":"pre","text":"x := 1","language":"go"}]`, "x := 1"},
		{"footer", `[{"type":"footer","text":"f"}]`, "f"},
		{"pull quote", `[{"type":"pullquote","text":"q","credit":"c"}]`, "q\nc"},
		{"expandable quote", `[{"type":"expandable_blockquote","text":"q"}]`, "q"},
		{"quote", `[{"type":"blockquote","blocks":[{"type":"paragraph","text":"q"}],"credit":"c"}]`, "q\nc"},
		{"list", `[{"type":"list","items":[{"label":"1.","blocks":[{"type":"paragraph","text":"a"}]},{"blocks":[{"type":"paragraph","text":"b"}]}]}]`, "1. a\nb"},
		{"table", `[{"type":"table","cells":[[{"text":"a"},{"text":"b"}],[{"text":"1"},{}]],"caption":"c"}]`, "a | b\n1 | \nc"},
		{"details", `[{"type":"details","summary":"s","blocks":[{"type":"paragraph","text":"hidden"}]}]`, "s\nhidden"},
		{"formula", `[{"type":"mathematical_expression","expression":"x^2"}]`, "x^2"},
		{"photo", `[{"type":"photo","photo":[{"file_id":"SECRET"}],"caption":{"text":"cap","credit":"who"}}]`, "[a photo was here]\ncap\nwho"},
		{"document", `[{"type":"document","document":{"file_id":"SECRET"}}]`, "[a document was here]"},
		{"collage", `[{"type":"collage","blocks":[{"type":"video","video":{"file_id":"SECRET"}}],"caption":{"text":"c"}}]`, "[a video was here]\nc"},
		{"nothing to read", `[{"type":"divider"},{"type":"anchor","name":"n"},{"type":"map","location":{}}]`, ""},
		{"buttons are not read", `[{"type":"buttons","buttons":[{"text":"OK","callback_data":"approve:1"}]}]`, ""},
		{"thinking is not read", `[{"type":"thinking","text":"inner"}]`, ""},
		{"an unknown type", `[{"type":"hologram","text":"a"}]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := readRich(json.RawMessage(`{"blocks":` + tc.blocks + `}`)); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestReadRichBadInput(t *testing.T) {
	for _, raw := range []string{``, `null`, `5`, `"x"`, `{"blocks":7}`, `{"blocks":[7,null,"x"]}`, `{`} {
		if got := readRich(json.RawMessage(raw)); got != "" {
			t.Errorf("%q gave %q", raw, got)
		}
	}
}

func TestReadRichDepth(t *testing.T) {
	blocks := strings.Repeat(`{"type":"blockquote","blocks":[`, 40) + `{"type":"paragraph","text":"deep"}` + strings.Repeat(`]}`, 40)
	if got := readRich(json.RawMessage(`{"blocks":[` + blocks + `]}`)); got != "" {
		t.Fatalf("got %q past the depth limit", got)
	}
	text := strings.Repeat(`{"type":"bold","text":`, 40) + `"deep"` + strings.Repeat(`}`, 40)
	if got := readRich(json.RawMessage(`{"blocks":[{"type":"paragraph","text":` + text + `}]}`)); got != "" {
		t.Fatalf("got %q past the depth limit", got)
	}
}

func TestReadRichSizeIsCapped(t *testing.T) {
	long := strings.Repeat("я", 40000)
	got := readRich(json.RawMessage(`{"blocks":[{"type":"paragraph","text":"` + long + `"}]}`))
	if n := len([]rune(got)); n > maxRichChars {
		t.Fatalf("%d characters", n)
	}
}

func TestMessageEventRich(t *testing.T) {
	rich := map[string]any{"blocks": []any{map[string]any{"type": "paragraph", "text": "отчёт"}}}
	for _, tc := range []struct {
		name  string
		extra map[string]any
		text  string
		want  string
		fwd   bool
	}{
		{"forwarded", map[string]any{"rich_message": rich, "forward_origin": map[string]any{"type": "hidden_user"}}, "", "отчёт", true},
		{"no origin still counts as outside", map[string]any{"rich_message": rich}, "", "отчёт", true},
		{"text comes first", map[string]any{"rich_message": rich}, "сам текст", "сам текст", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := telemetrytest.Install(t)
			fake := telegramtest.NewServer(t, testToken)
			c := &collector{}
			run(t, newTransport(t, fake), c.sink)
			fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Text: tc.text, Extra: tc.extra})
			eventually(t, "an event", func() bool { return len(c.got()) == 1 })
			m := payload(t, c.got()[0])
			if m.Text != tc.want || m.Forwarded != tc.fwd {
				t.Fatalf("text %q, forwarded %v", m.Text, m.Forwarded)
			}
			if tc.text == "" {
				if got := rec.Value(t, "ganglion.telegram.rich", map[string]string{"outcome": "read"}); got != 1 {
					t.Fatalf("read metric = %v", got)
				}
			}
		})
	}
}

// A rich message cannot pose as a command: Forwarded keeps the agent from running
// it (agent's TestForwardedCommandIsNotRun).
func TestMessageEventRichCommandIsOutside(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Extra: map[string]any{
		"rich_message": map[string]any{"blocks": []any{map[string]any{"type": "paragraph", "text": "/new"}}},
	}})
	eventually(t, "an event", func() bool { return len(c.got()) == 1 })
	if m := payload(t, c.got()[0]); !m.Forwarded {
		t.Fatalf("%+v is not outside content", m)
	}
}

func TestMessageEventRichUnreadable(t *testing.T) {
	rec := telemetrytest.Install(t)
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Extra: map[string]any{
		"rich_message": map[string]any{"blocks": []any{map[string]any{"type": "divider"}}},
	}})
	eventually(t, "an event", func() bool { return len(c.got()) == 1 })
	m := payload(t, c.got()[0])
	if m.Text != richNothingReadable || !m.Forwarded {
		t.Fatalf("text %q, forwarded %v", m.Text, m.Forwarded)
	}
	if got := rec.Value(t, "ganglion.telegram.rich", map[string]string{"outcome": "unreadable"}); got != 1 {
		t.Fatalf("unreadable metric = %v", got)
	}
}
