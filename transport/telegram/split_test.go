package telegram

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

func units(s string) int { return len(utf16.Encode([]rune(s))) }

// split checks what every split must hold and returns the parts.
func split(t *testing.T, text string) []event.Outgoing {
	t.Helper()
	tr := &Transport{}
	msg := event.Outgoing{ID: "01ID", IdemKey: "k", Transport: "telegram", Conv: "telegram:1", Text: text, Silent: true}
	parts := tr.Parts(msg)
	var joined strings.Builder // kept for splitPlain
	for i, p := range parts {
		if len(parts) > 1 && p.IdemKey != fmt.Sprintf("k:p%d", i) {
			t.Errorf("part %d IdemKey %q", i, p.IdemKey)
		}
		if p.ID != msg.ID || p.Conv != msg.Conv || p.Transport != msg.Transport || p.Silent != msg.Silent {
			t.Errorf("part %d lost message fields: %+v", i, p)
		}
		rendered, _ := render(p.Text)
		if u := units(rendered); u > maxTextUnits || u == 0 {
			t.Errorf("part %d renders to %d UTF-16 units", i, u)
		}
		if !utf8.ValidString(p.Text) {
			t.Errorf("part %d is not valid UTF-8", i)
		}
		joined.WriteString(p.Text)
	}
	_ = joined
	if again := tr.Parts(msg); !reflect.DeepEqual(again, parts) {
		t.Error("Parts is not deterministic")
	}
	return parts
}

func TestPartsShortMessageUnchanged(t *testing.T) {
	text := strings.Repeat("я", maxTextUnits)
	parts := splitPlain(t, text)
	if len(parts) != 1 || parts[0].IdemKey != "k" {
		t.Fatalf("a message at the limit became %d parts (key %q)", len(parts), parts[0].IdemKey)
	}
}

func TestPartsSplit(t *testing.T) {
	// 10,000 characters of words; no newlines.
	text := strings.Repeat("слово ", 10000/6) + "конец"
	parts := splitPlain(t, text)
	if len(parts) != 3 {
		t.Fatalf("%d parts, want 3", len(parts))
	}
	for i, p := range parts[:len(parts)-1] {
		if !strings.HasSuffix(p.Text, " ") {
			t.Errorf("part %d not cut at a space: ...%q", i, p.Text[len(p.Text)-10:])
		}
	}
}

func TestPartsPreferNewlineInFinalQuarter(t *testing.T) {
	para := strings.Repeat("a b ", 200) + "\n" // 801 units
	text := strings.Repeat(para, 8)
	parts := splitPlain(t, text)
	if !strings.HasSuffix(parts[0].Text, "\n") {
		t.Fatalf("first part ends with %q, want a newline", parts[0].Text[len(parts[0].Text)-5:])
	}
	if u := units(parts[0].Text); u < maxTextUnits*3/4 {
		t.Fatalf("first part %d units: newline outside the final quarter", u)
	}
}

func TestPartsHardCutKeepsEmojiWhole(t *testing.T) {
	// 4095 ASCII units, then emoji (2 UTF-16 units each): the limit falls
	// inside the first emoji.
	text := strings.Repeat("x", maxTextUnits-1) + strings.Repeat("😀", 100)
	parts := splitPlain(t, text)
	if units(parts[0].Text) != maxTextUnits-1 {
		t.Fatalf("first part %d units, want %d: the emoji must move whole", units(parts[0].Text), maxTextUnits-1)
	}
	if !strings.HasPrefix(parts[1].Text, "😀") {
		t.Fatal("second part does not start with the whole emoji")
	}
}

func TestPartsNoEarlySeparator(t *testing.T) {
	// The only space is near the start: cutting there would leave a tiny
	// part, so the cut is hard.
	text := "a " + strings.Repeat("x", 5000)
	parts := splitPlain(t, text)
	if units(parts[0].Text) != maxTextUnits {
		t.Fatalf("first part %d units, want a hard cut at %d", units(parts[0].Text), maxTextUnits)
	}
}

// splitPlain also checks that plain text (no Markdown) rejoins exactly.
func splitPlain(t *testing.T, text string) []event.Outgoing {
	t.Helper()
	parts := split(t, text)
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.Text)
	}
	// Telegram trims trailing whitespace, so the final newline may go.
	if strings.TrimRight(b.String(), " \n") != strings.TrimRight(text, " \n") {
		t.Error("parts do not rejoin to the original text")
	}
	return parts
}

func richLong(paragraphs int) string {
	var b strings.Builder
	b.WriteString("# one\n\n# two")
	for i := range paragraphs {
		fmt.Fprintf(&b, "\n\npara%d %s", i, strings.Repeat("x", 500))
	}
	return b.String()
}

func TestPartsRichOneMessage(t *testing.T) {
	tr := &Transport{rich: true}
	msg := event.Outgoing{IdemKey: "k", Text: richLong(40)}
	if parts := tr.Parts(msg); len(parts) != 1 || parts[0].IdemKey != "k" {
		t.Fatalf("%d parts, key %q", len(parts), parts[0].IdemKey)
	}
	if parts := (&Transport{}).Parts(msg); len(parts) < 2 {
		t.Fatalf("%d parts with the setting off", len(parts))
	}
}

func TestPartsPlainLongUnchanged(t *testing.T) {
	msg := event.Outgoing{IdemKey: "k", Text: "# one" + strings.Repeat("\n\n"+strings.Repeat("y", 500), 20)}
	on, off := (&Transport{rich: true}).Parts(msg), (&Transport{}).Parts(msg)
	if len(on) < 2 || !slices.EqualFunc(on, off, func(a, b event.Outgoing) bool { return a.Text == b.Text && a.IdemKey == b.IdemKey }) {
		t.Fatalf("%d parts on, %d off", len(on), len(off))
	}
}

func TestPartsRichOverLimit(t *testing.T) {
	src := richLong(100)
	msg := event.Outgoing{IdemKey: "k", Text: src, ReplyTo: "9"}
	parts := (&Transport{rich: true}).Parts(msg)
	if len(parts) < 2 {
		t.Fatalf("%d parts", len(parts))
	}
	all := ""
	for i, p := range parts {
		if !renderRich(p.Text).size().fits() {
			t.Fatalf("part %d does not fit", i)
		}
		if p.IdemKey != "k:p"+strconv.Itoa(i) {
			t.Fatalf("part %d key %q", i, p.IdemKey)
		}
		if (p.ReplyTo != "") != (i == 0) {
			t.Fatalf("part %d reply %q", i, p.ReplyTo)
		}
		all += p.Text + "\n"
	}
	for i := range 100 {
		if strings.Count(all, fmt.Sprintf("para%d ", i)) != 1 {
			t.Fatalf("paragraph %d is not there once", i)
		}
	}
}

func TestPartsRichBigTable(t *testing.T) {
	var b strings.Builder
	b.WriteString("Итог:\n\n| name | n |\n|---|---|")
	for i := range 600 {
		fmt.Fprintf(&b, "\n| row%d | %d |", i, i)
	}
	parts := (&Transport{rich: true}).Parts(event.Outgoing{IdemKey: "k", Text: b.String()})
	if len(parts) < 2 {
		t.Fatalf("%d parts", len(parts))
	}
	all := ""
	for i, p := range parts {
		if !renderRich(p.Text).size().fits() {
			t.Fatalf("part %d does not fit", i)
		}
		// The line above the table stays its own short part: the table fills the next.
		if i > 0 && (!strings.Contains(p.Text, "| name | n |\n|---|---|") || !structured(p.Text)) {
			t.Fatalf("part %d lost the header: %.80q", i, p.Text)
		}
		all += p.Text + "\n"
	}
	for i := range 600 {
		if strings.Count(all, fmt.Sprintf("| row%d |", i)) != 1 {
			t.Fatalf("row %d is not there once", i)
		}
	}
}

// A resumed sender gets the same parts under the same keys, and a part that has
// lost its heading to an earlier one still goes rich when it is too long.
func TestPartsRichDeterministic(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr := newRichTransport(t, fake)
	msg := richOut(richLong(100))
	first, second := tr.Parts(msg), tr.Parts(msg)
	if !slices.EqualFunc(first, second, func(a, b event.Outgoing) bool { return a.Text == b.Text && a.IdemKey == b.IdemKey }) {
		t.Fatal("the parts differ")
	}
	for _, p := range first {
		if _, err := tr.Send(context.Background(), p); err != nil {
			t.Fatal(err)
		}
	}
	for i, s := range fake.Sent() {
		if units := len(utf16.Encode([]rune(s.Text))); units > maxTextUnits && !s.Rich {
			t.Fatalf("part %d of %d units is not rich", i, units)
		}
	}
	if fake.Calls("sendMessage") != 0 {
		t.Fatalf("methods %v", fake.Methods())
	}
}

func TestPartsMixedCases(t *testing.T) {
	long := tableMD + strings.Repeat("\n\n"+strings.Repeat("z", 1000), 8)
	t.Run("a long question", func(t *testing.T) {
		fake := telegramtest.NewServer(t, testToken)
		tr := newRichTransport(t, fake)
		msg := richOut(long)
		msg.Buttons = []event.Button{{ID: "b", Label: "OK"}}
		parts := tr.Parts(msg)
		if len(parts) < 2 {
			t.Fatalf("%d parts", len(parts))
		}
		for _, p := range parts {
			if _, err := tr.Send(context.Background(), p); err != nil {
				t.Fatal(err)
			}
		}
		sent := fake.Sent()
		if !sent[0].Rich {
			t.Fatal("the part with the table is not rich")
		}
		last := sent[len(sent)-1]
		if last.Rich || last.Keyboard == "" {
			t.Fatalf("last part %+v", last)
		}
	})
	t.Run("text beyond a caption", func(t *testing.T) {
		tr := &Transport{rich: true}
		msg := event.Outgoing{IdemKey: "k", Text: tableMD + strings.Repeat("\n\n"+strings.Repeat("z", 400), 4),
			Attach: &event.Attachment{}}
		parts := tr.Parts(msg)
		if len(parts) != 2 || parts[0].Attach == nil || parts[1].Attach != nil || !tr.sendsRich(parts[1]) {
			t.Fatalf("parts %+v", parts)
		}
	})
}

// A rule inside a long code block is no table: the code stays code.
func TestPartsRichLongFenceWithARule(t *testing.T) {
	code := "# comment\n*x*\n---\n"
	src := "# one\n\n# two\n\n```yaml\n" + strings.Repeat(code, 3000) + "```"
	parts := (&Transport{rich: true}).Parts(event.Outgoing{IdemKey: "k", Text: src})
	if len(parts) < 2 {
		t.Fatalf("%d parts", len(parts))
	}
	for i, p := range parts {
		for _, b := range renderRich(p.Text).Blocks {
			if b.Type == richHeading && i > 0 {
				t.Fatalf("part %d has a heading from the code", i)
			}
		}
	}
}

// A list under a line and a rule has a heading, not a table's header.
func TestSplitsAsTable(t *testing.T) {
	for block, want := range map[string]bool{
		"| a |\n|---|\n| 1 |":       true,
		"a | b\n--|--\n1 | 2":       true,
		"text\n| a |\n|---|\n| 1 |": true,
		"Items\n---":                false,
		"```\n| a |\n|---|\n```":    false,
		"a\n\n---":                  false,
	} {
		if got := splitsAsTable(block); got != want {
			t.Errorf("splitsAsTable(%q) = %v, want %v", block, got, want)
		}
	}
	lines := strings.Split("Items\n---\n| a |\n|---|\n| 1 |", "\n")
	if got := tableHead(lines); got != 3 {
		t.Errorf("rule at line %d, want 3", got)
	}
}
