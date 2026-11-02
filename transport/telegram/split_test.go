package telegram

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
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
	if again := tr.Parts(msg); !slices.Equal(again, parts) {
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
