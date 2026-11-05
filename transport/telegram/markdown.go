package telegram

import (
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// entity is a Bot API MessageEntity; offsets and lengths are UTF-16 units.
type entity struct {
	Type     string `json:"type"`
	Offset   int    `json:"offset"`
	Length   int    `json:"length"`
	URL      string `json:"url,omitempty"`
	Language string `json:"language,omitempty"`
}

// expandableQuote is the length above which a quote is sent collapsed.
const expandableQuote = 500

var md = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough))

// render turns Markdown into the visible text and the entities that format
// it (ADR-0025). Nothing it produces can be rejected by Telegram as a
// parse error; at worst formatting is lost.
func render(src string) (string, []entity) {
	source := []byte(src)
	doc := md.Parser().Parse(text.NewReader(source))
	w := &writer{src: source}
	w.blocks(doc, "", "\n\n")
	return w.finish()
}

type writer struct {
	src  []byte
	b    strings.Builder
	n    int // UTF-16 units written
	ents []entity
}

func (w *writer) write(s string) {
	w.b.WriteString(s)
	w.n += len(utf16.Encode([]rune(s)))
}

// span records an entity over what fn writes, if it wrote anything.
func (w *writer) span(e entity, fn func()) {
	start := w.n
	fn()
	if w.n > start {
		e.Offset, e.Length = start, w.n-start
		w.ents = append(w.ents, e)
	}
}

// finish trims trailing whitespace and clips entities to the text.
func (w *writer) finish() (string, []entity) {
	s := strings.TrimRight(w.b.String(), " \n\t")
	n := len(utf16.Encode([]rune(s)))
	var out []entity
	for _, e := range w.ents {
		if e.Offset >= n {
			continue
		}
		if e.Offset+e.Length > n {
			e.Length = n - e.Offset
		}
		out = append(out, e)
	}
	return s, out
}

// blocks writes the block children of n, separated by sep; indent prefixes
// continuation lines of list items.
func (w *writer) blocks(n ast.Node, indent, sep string) {
	first := true
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if !first {
			w.write(sep)
		}
		first = false
		w.block(c, indent)
	}
}

func (w *writer) lines(n ast.Node) string {
	var b strings.Builder
	l := n.Lines()
	for i := 0; i < l.Len(); i++ {
		seg := l.At(i)
		b.Write(seg.Value(w.src))
	}
	return b.String()
}

func (w *writer) block(n ast.Node, indent string) {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		w.inlines(n)
	case *ast.Heading:
		w.span(entity{Type: "bold"}, func() { w.inlines(n) })
	case *ast.FencedCodeBlock:
		code := strings.TrimRight(w.lines(n), "\n")
		w.span(entity{Type: entityPre, Language: string(n.Language(w.src))}, func() { w.write(code) })
	case *ast.CodeBlock:
		code := strings.TrimRight(w.lines(n), "\n")
		w.span(entity{Type: entityPre}, func() { w.write(code) })
	case *ast.Blockquote:
		w.quote(n, indent)
	case *ast.List:
		w.list(n, indent)
	case *ast.ThematicBreak:
		w.write("———")
	case *ast.HTMLBlock:
		w.write(strings.TrimRight(w.lines(n), "\n"))
	case *east.Table:
		w.span(entity{Type: entityPre}, func() { w.write(w.table(n)) })
	default:
		w.blocks(n, indent, "\n")
	}
}

// entityPre is Telegram's preformatted block: code and tables.
const entityPre = "pre"

// The two kinds of quote Telegram has. It does not nest them.
const (
	entityQuote      = "blockquote"
	entityExpandable = "expandable_blockquote"
)

// collapsed returns ents for text shown whole inside one expandable quote
// (R-LIVE-5), whatever its length. A quote inside the text is dropped, since
// quotes do not nest; other formatting stays.
func collapsed(text string, ents []entity) []entity {
	n := len(utf16.Encode([]rune(text)))
	if n == 0 {
		return nil
	}
	out := []entity{{Type: entityExpandable, Offset: 0, Length: n}}
	for _, e := range ents {
		if e.Type != entityQuote && e.Type != entityExpandable {
			out = append(out, e)
		}
	}
	return out
}

// quote renders a blockquote; a long one is expandable.
func (w *writer) quote(n *ast.Blockquote, indent string) {
	start, ents := w.n, len(w.ents)
	w.blocks(n, indent, "\n")
	typ := entityQuote
	if w.n-start > expandableQuote {
		typ = entityExpandable
	}
	if w.n > start {
		q := entity{Type: typ, Offset: start, Length: w.n - start}
		w.ents = append(w.ents[:ents], append([]entity{q}, w.ents[ents:]...)...)
	}
}

// list renders a list with bullets or numbers, items on their own lines.
func (w *writer) list(n *ast.List, indent string) {
	num := n.Start
	for item := n.FirstChild(); item != nil; item = item.NextSibling() {
		if item != n.FirstChild() {
			w.write("\n" + indent)
		}
		marker := "• "
		if n.IsOrdered() {
			marker = strconv.Itoa(num) + ". "
			num++
		}
		w.write(marker)
		w.blocks(item, indent+"  ", "\n"+indent+"  ")
	}
}

// table lays a table out in monospace columns.
func (w *writer) table(t *east.Table) string {
	var rows [][]string
	for r := t.FirstChild(); r != nil; r = r.NextSibling() {
		var row []string
		for c := r.FirstChild(); c != nil; c = c.NextSibling() {
			cw := &writer{src: w.src}
			cw.inlines(c)
			s, _ := cw.finish()
			row = append(row, s)
		}
		rows = append(rows, row)
	}
	var widths []int
	for _, row := range rows {
		for i, c := range row {
			if i >= len(widths) {
				widths = append(widths, 0)
			}
			widths[i] = max(widths[i], len([]rune(c)))
		}
	}
	line := func(row []string) string {
		cells := make([]string, len(widths))
		for i := range widths {
			c := ""
			if i < len(row) {
				c = row[i]
			}
			cells[i] = c + strings.Repeat(" ", widths[i]-len([]rune(c)))
		}
		return strings.TrimRight(strings.Join(cells, " | "), " ")
	}
	var out []string
	for i, row := range rows {
		out = append(out, line(row))
		if i == 0 {
			dashes := make([]string, len(widths))
			for j, wd := range widths {
				dashes[j] = strings.Repeat("-", max(wd, 1))
			}
			out = append(out, strings.Join(dashes, "-+-"))
		}
	}
	return strings.Join(out, "\n")
}

func (w *writer) inlines(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		w.inline(c)
	}
}

func (w *writer) inline(n ast.Node) {
	switch n := n.(type) {
	case *ast.Text:
		v := n.Segment.Value(w.src)
		if !n.IsRaw() {
			v = util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(v)))
		}
		w.write(string(v))
		if n.SoftLineBreak() || n.HardLineBreak() {
			w.write("\n")
		}
	case *ast.String:
		w.write(string(n.Value))
	case *ast.Emphasis:
		typ := "italic"
		if n.Level >= 2 {
			typ = "bold"
		}
		w.span(entity{Type: typ}, func() { w.inlines(n) })
	case *east.Strikethrough:
		w.span(entity{Type: "strikethrough"}, func() { w.inlines(n) })
	case *ast.CodeSpan:
		var b strings.Builder
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				b.Write(t.Segment.Value(w.src))
			}
		}
		w.span(entity{Type: "code"}, func() { w.write(b.String()) })
	case *ast.Link:
		w.span(entity{Type: "text_link", URL: string(n.Destination)}, func() { w.inlines(n) })
	case *ast.Image:
		w.span(entity{Type: "text_link", URL: string(n.Destination)}, func() { w.inlines(n) })
	case *ast.AutoLink:
		w.write(string(n.URL(w.src))) // Telegram links URLs by itself
	case *ast.RawHTML:
		for i := 0; i < n.Segments.Len(); i++ {
			seg := n.Segments.At(i)
			w.write(string(seg.Value(w.src)))
		}
	default:
		w.inlines(n)
	}
}
