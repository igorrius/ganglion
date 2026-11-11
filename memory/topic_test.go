package memory

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var nov11 = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)

func TestTopicRoundTrip(t *testing.T) {
	in := Topic{Slug: "inverter", Title: "Inverter", Summary: "The Deye inverter and its battery", Next: 4, Facts: []Fact{
		{ID: "f1", Text: "Negative batt_w on the Deye is the battery charging.", Date: nov11, Turn: "01M7ABC"},
		{ID: "f2", Text: "The battery is 10 kWh.", Date: nov11.AddDate(0, 0, 1), Turn: "01M8XYZ", Tainted: true},
		{ID: "f3", Text: "The inverter is in the garage."},
	}}
	data, err := encodeTopic(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(data), "\n") || strings.HasSuffix(string(data), "\n\n") {
		t.Fatalf("the file must end with one newline:\n%s", data)
	}
	out, err := decodeTopic("inverter", data)
	if err != nil {
		t.Fatal(err)
	}
	if !sameTopic(in, out) {
		t.Fatalf("round trip\n in  %+v\n out %+v\n%s", in, out, data)
	}
}

func TestDecodeTopicLines(t *testing.T) {
	cases := []struct {
		name string
		line string
		want Fact
	}{
		{"full", "- [f2] text <!-- 2026-11-11 turn=01M tainted -->", Fact{ID: "f2", Text: "text", Date: nov11, Turn: "01M", Tainted: true}},
		{"date only", "- [f7] text <!-- 2026-11-11 -->", Fact{ID: "f7", Text: "text", Date: nov11}},
		{"bullet, no id", "- text", Fact{Text: "text"}},
		{"no bullet", "text", Fact{Text: "text"}},
		{"id prefix in the text", "- [f2] [f9] text", Fact{ID: "f2", Text: "[f9] text"}},
		{"bad comment stays text", "- [f1] text <!-- yesterday -->", Fact{ID: "f1", Text: "text <!-- yesterday -->"}},
		{"unknown token stays text", "- [f1] text <!-- 2026-11-11 by=me -->", Fact{ID: "f1", Text: "text <!-- 2026-11-11 by=me -->"}},
		{"zero id is text", "- [f0] text", Fact{Text: "[f0] text"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tp, err := decodeTopic("x", []byte("---\ntitle: X\n---\n\n"+c.line+"\n"))
			if err != nil {
				t.Fatal(err)
			}
			if len(tp.Facts) != 1 || !sameFact(tp.Facts[0], c.want) {
				t.Fatalf("got %+v, want %+v", tp.Facts, c.want)
			}
		})
	}
}

func TestDecodeTopicFile(t *testing.T) {
	tp, err := decodeTopic("car", []byte("The car is blue.\n\n  \n- It is serviced in May.\n"))
	if err != nil {
		t.Fatal(err)
	}
	if tp.Title != "car" || len(tp.Facts) != 2 || tp.Facts[1].Text != "It is serviced in May." {
		t.Fatalf("a file with no frontmatter: %+v", tp)
	}
	_, err = decodeTopic("car", []byte("---\ntitle: Car\n- no end\n"))
	if !errors.Is(err, errFrontmatter) {
		t.Fatalf("a frontmatter that does not end: %v", err)
	}
	tp, err = decodeTopic("car", []byte("---\ntitle: \"Car\n  model\"\nsummary: \"one\n two\"\n---\n- [f3] x\n- [f12] y\n- z\n"))
	if err != nil {
		t.Fatal(err)
	}
	if tp.Title != "Car model" || tp.Summary != "one two" {
		t.Fatalf("title and summary on one line: %q %q", tp.Title, tp.Summary)
	}
	if tp.Next != 13 {
		t.Fatalf("next without the key: %d, want 13", tp.Next)
	}
	tp, err = decodeTopic("car", []byte("---\ntitle: Car\nnext: 20\n---\n- [f3] x\n"))
	if err != nil || tp.Next != 20 {
		t.Fatalf("next kept: %d %v", tp.Next, err)
	}
	tp, err = decodeTopic("car", []byte("---\ntitle: Car\nnext: 2\n---\n- [f3] x\n"))
	if err != nil || tp.Next != 4 {
		t.Fatalf("next below an id: %d %v", tp.Next, err)
	}
	tp, err = decodeTopic("car", []byte("---\ntitle: Car\n---\n"))
	if err != nil || tp.Next != 1 || len(tp.Facts) != 0 {
		t.Fatalf("an empty topic: %+v %v", tp, err)
	}
}

func TestCheckFactText(t *testing.T) {
	cases := []struct {
		name, in, want string
		bad            bool
	}{
		{"plain", "The boiler is in the basement.", "The boiler is in the basement.", false},
		{"line breaks", "one\ntwo\r\n  three", "one two three", false},
		{"empty", "  \n ", "", true},
		{"300 runes", strings.Repeat("ж", 300), strings.Repeat("ж", 300), false},
		{"301 runes", strings.Repeat("ж", 301), "", true},
		{"opens a comment", "a <!-- b", "", true},
		{"closes a comment", "a --> b", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := checkFactText(c.in)
			if c.bad {
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("want ErrInvalid, got %q %v", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Fatalf("got %q %v, want %q", got, err, c.want)
			}
		})
	}
}

func TestTopicChanged(t *testing.T) {
	tp := Topic{Facts: []Fact{{Text: "a", Date: nov11}, {Text: "b", Date: nov11.AddDate(0, 0, 3)}, {Text: "c"}}}
	if !tp.Changed().Equal(nov11.AddDate(0, 0, 3)) {
		t.Fatalf("changed %v", tp.Changed())
	}
	if !(Topic{Facts: []Fact{{Text: "c"}}}).Changed().IsZero() {
		t.Fatal("only hand-written facts: zero")
	}
}

func sameTopic(a, b Topic) bool {
	if a.Slug != b.Slug || a.Title != b.Title || a.Summary != b.Summary || a.Next != b.Next || len(a.Facts) != len(b.Facts) {
		return false
	}
	for i := range a.Facts {
		if !sameFact(a.Facts[i], b.Facts[i]) {
			return false
		}
	}
	return true
}

func sameFact(a, b Fact) bool {
	return a.ID == b.ID && a.Text == b.Text && a.Date.Equal(b.Date) && a.Turn == b.Turn && a.Tainted == b.Tainted
}

// Review fix 8: a file saved with CRLF line ends, or with a BOM, reads as
// one with plain line ends.
func TestDecodeTopicCRLF(t *testing.T) {
	tp, err := decodeTopic("car", []byte("\ufeff---\r\ntitle: Car\r\nnext: 3\r\n---\r\n\r\n- [f2] The car is blue.\r\n"))
	if err != nil || tp.Title != "Car" || tp.Next != 3 || len(tp.Facts) != 1 || tp.Facts[0].Text != "The car is blue." {
		t.Fatalf("%+v %v", tp, err)
	}
}
