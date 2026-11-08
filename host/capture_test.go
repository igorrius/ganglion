package host

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// keep hides nothing.
func keep(s string) string { return s }

func TestAShortWriteKeepsEverything(t *testing.T) {
	c := newCapture(16, 16)
	_, _ = c.Write([]byte("hello\n"))
	if got, cut := c.text(keep); got != "hello\n" || cut {
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
	got, cut := c.text(keep)
	want := s[:16] + "\n… 968 bytes left out …\n" + s[len(s)-16:]
	if got != want || !cut || c.total != 1000 {
		t.Fatalf("text = %q, %v; want %q", got, cut, want)
	}
}

// Past the head and its edge, output in the middle is dropped as it comes.
func TestAFloodKeepsOnlyTheEdges(t *testing.T) {
	c := newCapture(16, 16)
	chunk := []byte(strings.Repeat("m", 4096))
	for range 1000 {
		_, _ = c.Write(chunk)
	}
	if len(c.head) != 16+edge || len(c.tail) > 2*(16+edge) {
		t.Fatalf("kept %d and %d bytes", len(c.head), len(c.tail))
	}
	if got, _ := c.text(keep); !strings.Contains(got, "4095968 bytes left out") {
		t.Fatalf("text = %q", got)
	}
}

// The head and the tail may cut a letter in two; the text stays valid.
func TestACutLetterIsDropped(t *testing.T) {
	c := newCapture(5, 5)
	_, _ = c.Write([]byte(strings.Repeat("привет ", 100)))
	got, cut := c.text(keep)
	if !cut || !utf8.ValidString(got) || !strings.HasPrefix(got, "пр\n") {
		t.Fatalf("text = %q, %v", got, cut)
	}
}

// A secret across the end of the head or the start of the tail is hidden
// whole, even when it came in pieces over several writes.
func TestASecretAcrossEitherCutIsHiddenWhole(t *testing.T) {
	for _, size := range []int{32, 64, 1024} {
		c := newCapture(size, size)
		out := []byte(straddling(size, size))
		for len(out) > 0 { // pipe chunks of 7 bytes cut the secrets too
			n := min(7, len(out))
			_, _ = c.Write(out[:n])
			out = out[n:]
		}
		got, cut := c.text(hideSecret)
		if !cut {
			t.Fatalf("size %d: nothing cut", size)
		}
		noPartOf(t, got)
	}
}
