package telegram

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/texts"
)

func ent(typ string, off, n int) entity { return entity{Type: typ, Offset: off, Length: n} }

func sortEnts(es []entity) []entity {
	out := slices.Clone(es)
	slices.SortFunc(out, func(a, b entity) int {
		if a.Offset != b.Offset {
			return a.Offset - b.Offset
		}
		return strings.Compare(a.Type, b.Type)
	})
	return out
}

func TestRender(t *testing.T) {
	long := strings.Repeat("слово ", 100)
	for _, tc := range []struct {
		name, md, text string
		ents           []entity
	}{
		{"bold", "Привет, **Игорь**!", "Привет, Игорь!", []entity{ent("bold", 8, 5)}},
		{"italic both ways", "*курсив* и _тоже_", "курсив и тоже", []entity{ent("italic", 0, 6), ent("italic", 9, 4)}},
		{"bold and italic", "***оба***", "оба", []entity{ent("bold", 0, 3), ent("italic", 0, 3)}},
		{"nested", "**жирный и *курсив* внутри**", "жирный и курсив внутри", []entity{ent("bold", 0, 22), ent("italic", 9, 6)}},
		{"strikethrough", "~~нет~~ да", "нет да", []entity{ent("strikethrough", 0, 3)}},
		{"inline code", "запусти `go test` сейчас", "запусти go test сейчас", []entity{ent("code", 8, 7)}},
		{"fenced code", "```go\nfmt.Println(1)\n```", "fmt.Println(1)", []entity{{Type: "pre", Offset: 0, Length: 14, Language: "go"}}},
		{"link", "см. [сайт](https://x.org)", "см. сайт", []entity{{Type: "text_link", Offset: 4, Length: 4, URL: "https://x.org"}}},
		{"heading", "# Заголовок\n\nтекст", "Заголовок\n\nтекст", []entity{ent("bold", 0, 9)}},
		{"bullets", "- a\n- b\n  - c", "• a\n• b\n  • c", nil},
		{"numbers", "1. a\n2. **b**", "1. a\n2. b", []entity{ent("bold", 8, 1)}},
		{"quote", "> цитата", "цитата", []entity{ent("blockquote", 0, 6)}},
		{"long quote", "> " + long, strings.TrimSpace(long), []entity{ent("expandable_blockquote", 0, len([]rune(strings.TrimSpace(long))))}},
		{"table", "| a | bb |\n|---|---|\n| 1 | 2 |", "a | bb\n--+---\n1 | 2", []entity{ent("pre", 0, 19)}},
		{"raw html is text", "<b>raw</b> text", "<b>raw</b> text", nil},
		{"emoji offsets", "😀 **x**", "😀 x", []entity{ent("bold", 3, 1)}},
		{"soft break kept", "a\nb", "a\nb", nil},
		{"paragraphs", "one\n\ntwo", "one\n\ntwo", nil},
		{"rule", "a\n\n---\n\nb", "a\n\n———\n\nb", nil},
		{"escaped", `2\*3\*4`, "2*3*4", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, ents := render(tc.md)
			if text != tc.text {
				t.Fatalf("text %q, want %q", text, tc.text)
			}
			if got, want := sortEnts(ents), sortEnts(tc.ents); !slices.Equal(got, want) {
				t.Fatalf("entities %+v, want %+v", got, want)
			}
		})
	}
}

func TestRenderEntitiesStayInsideText(t *testing.T) {
	for _, md := range []string{"**a**   ", "> q\n\n", "```\ncode\n```\n\n\n", "- **x**\n"} {
		text, ents := render(md)
		n := textUnits([]rune(text))
		for _, e := range ents {
			if e.Offset < 0 || e.Length <= 0 || e.Offset+e.Length > n {
				t.Errorf("%q: entity %+v outside %d units of %q", md, e, n, text)
			}
		}
	}
}

func TestPartsKeepBlocksWhole(t *testing.T) {
	para := strings.Repeat("**слово** ", 150) // ~900 visible units per paragraph
	var blocks []string
	for i := range 8 {
		blocks = append(blocks, fmt.Sprintf("Абзац %d: %s", i, para))
	}
	md := strings.Join(blocks, "\n\n")
	parts := split(t, md)
	if len(parts) < 2 {
		t.Fatalf("%d parts", len(parts))
	}
	for i, p := range parts {
		text, _ := render(p.Text)
		if u := textUnits([]rune(text)); u > maxTextUnits {
			t.Fatalf("part %d renders to %d units", i, u)
		}
		if !strings.HasPrefix(p.Text, "Абзац ") {
			t.Fatalf("part %d does not start at a block: %q", i, p.Text[:20])
		}
	}
}

func TestPartsSplitLongCodeBlock(t *testing.T) {
	var lines []string
	for i := range 400 {
		lines = append(lines, fmt.Sprintf("line %03d: some code here", i))
	}
	md := "```python\n" + strings.Join(lines, "\n") + "\n```"
	parts := split(t, md)
	if len(parts) < 2 {
		t.Fatalf("%d parts", len(parts))
	}
	var got []string
	for i, p := range parts {
		text, ents := render(p.Text)
		if len(ents) != 1 || ents[0].Type != "pre" || ents[0].Language != "python" {
			t.Fatalf("part %d entities %+v", i, ents)
		}
		got = append(got, text)
	}
	if strings.Join(got, "\n") != strings.Join(lines, "\n") {
		t.Fatal("the code lines do not rejoin")
	}
}

func TestCollapsedWrapsAllTextInOneExpandableQuote(t *testing.T) {
	text, ents := render("short")
	got := collapsed(text, ents)
	if want := []entity{ent(entityExpandable, 0, 5)}; !slices.Equal(got, want) {
		t.Fatalf("entities %+v, want %+v", got, want)
	}
}

// Offsets count UTF-16 units, so an emoji counts twice.
func TestCollapsedCountsUTF16Units(t *testing.T) {
	if got, want := collapsed("😀 x", nil), []entity{ent(entityExpandable, 0, 4)}; !slices.Equal(got, want) {
		t.Fatalf("entities %+v, want %+v", got, want)
	}
}

// Telegram does not nest quotes, so one inside the answer must go, short or
// long, while other formatting stays.
func TestCollapsedRemovesQuotesInside(t *testing.T) {
	long := strings.Repeat("слово ", 100)
	for name, src := range map[string]string{
		"short quote": "**bold** text\n\n> quoted",
		"long quote":  "**bold** text\n\n> " + long,
	} {
		t.Run(name, func(t *testing.T) {
			text, ents := render(src)
			got := collapsed(text, ents)
			want := []entity{ent(entityExpandable, 0, len([]rune(text))), ent("bold", 0, 4)}
			if !slices.Equal(sortEnts(got), sortEnts(want)) {
				t.Fatalf("entities %+v, want %+v", got, want)
			}
		})
	}
}

func TestCollapsedOfNothingIsNothing(t *testing.T) {
	if got := collapsed("", nil); len(got) != 0 {
		t.Fatalf("entities %+v", got)
	}
}

// A fixed text's template is escaped by package texts; here it must come out as it was typed.
func TestEscapedTextShowsItsMarks(t *testing.T) {
	c := texts.Shipped()
	if got := c.Text(texts.English, texts.ReplyNoSuchResume, nil); got != "No such session; see /sessions." {
		t.Fatalf("%q", got)
	}
	cat, err := texts.New(texts.WithFiles(map[texts.Lang]map[texts.Key]string{"pl": {texts.ToastUsed: "a *b* _c_ `d` [e](f) #g <h> & i"}}))
	if err != nil {
		t.Fatal(err)
	}
	text, ents := render(cat.Text("pl", texts.ToastUsed, nil))
	if text != "a *b* _c_ `d` [e](f) #g <h> & i" || len(ents) != 0 {
		t.Fatalf("%q, %+v", text, ents)
	}
}
