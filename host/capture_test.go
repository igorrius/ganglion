package host

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestAShortWriteKeepsEverything(t *testing.T) {
	c := newCapture(16, 16)
	_, _ = c.Write([]byte("hello\n"))
	if got, cut := c.text(); got != "hello\n" || cut {
		t.Fatalf("text = %q, %v", got, cut)
	}
}

func TestALongOutputKeepsTheHeadAndTheTail(t *testing.T) {
	c := newCapture(16, 16)
	var all strings.Builder
	for i := range 10 {
		chunk := strings.Repeat(string(rune('a'+i)), 100)
		all.WriteString(chunk)
		if n, err := c.Write([]byte(chunk)); n != 100 || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	s := all.String()
	got, cut := c.text()
	want := s[:16] + "\n… 968 bytes left out …\n" + s[len(s)-16:]
	if got != want || !cut || c.total != 1000 {
		t.Fatalf("text = %q, %v; want %q", got, cut, want)
	}
}

// The head and the tail may cut a letter in two; render keeps valid text.
func TestACutLetterIsDroppedByRender(t *testing.T) {
	c := newCapture(5, 5)
	_, _ = c.Write([]byte(strings.Repeat("привет ", 10)))
	raw, _ := c.text()
	if utf8.ValidString(raw) {
		t.Fatalf("the test needs a cut letter: %q", raw)
	}
	if got := strings.ToValidUTF8(raw, ""); !utf8.ValidString(got) || !strings.Contains(got, "left out") {
		t.Fatalf("valid = %q", got)
	}
}
