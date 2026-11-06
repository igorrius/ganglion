package telegram

import (
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// The limits of a rich message (Bot API, "Rich Message Limits").
const (
	maxRichChars   = 32768
	maxRichBlocks  = 500 // nested blocks, list items and table rows count
	maxRichDepth   = 16
	maxRichColumns = 20
)

// minRichHeadings is how many headings make a message a report: one reads well
// enough as a bold line in an ordinary message (ADR-0054).
const minRichHeadings = 2

// richKind is the type of a block or of a piece of rich text. This list is the
// allowlist of ADR-0054: nothing else can reach Telegram, so no button, media
// block or formula can come from the model's text.
type richKind string

const (
	richParagraph  richKind = "paragraph"
	richHeading    richKind = "heading"
	richPre        richKind = "pre"
	richList       richKind = "list"
	richQuote      richKind = "blockquote"
	richExpandable richKind = "expandable_blockquote"
	richDivider    richKind = "divider"
	richTable      richKind = "table"

	richBold   richKind = "bold"
	richItalic richKind = "italic"
	richStrike richKind = "strikethrough"
	richCode   richKind = "code"
	richURL    richKind = "url"
)

// richMessage is an InputRichMessage described by blocks, never by its markdown
// or html fields, which Telegram parses (ADR-0054).
type richMessage struct {
	Blocks []richBlock `json:"blocks"`
}

// richBlock is an InputRichBlock of one of the kinds above.
type richBlock struct {
	Type       richKind     `json:"type"`
	Text       any          `json:"text,omitempty"` // rich text: a string, a richSpan or a list of them
	Size       int          `json:"size,omitempty"`
	Language   string       `json:"language,omitempty"`
	Items      []richItem   `json:"items,omitempty"`
	Blocks     []richBlock  `json:"blocks,omitempty"`
	Cells      [][]richCell `json:"cells,omitempty"`
	IsBordered bool         `json:"is_bordered,omitempty"`
}

// richItem is an InputRichBlockListItem.
type richItem struct {
	Blocks []richBlock `json:"blocks"`
	Value  int         `json:"value,omitempty"`
	Type   string      `json:"type,omitempty"`
}

// richCell is a RichBlockTableCell.
type richCell struct {
	Text     any    `json:"text"`
	IsHeader bool   `json:"is_header,omitempty"`
	Align    string `json:"align"`
	VAlign   string `json:"valign"`
}

// richSpan is a formatted piece of rich text.
type richSpan struct {
	Type richKind `json:"type"`
	Text any      `json:"text"`
	URL  string   `json:"url,omitempty"`
}

// orderedLabel is the label type of an item of an ordered list: decimal.
const orderedLabel = "1"

// emptyCell stands for an empty table cell, since a cell with no text is
// invisible rather than empty.
const emptyCell = " "

// renderRich builds the blocks of a rich message from Markdown. It is pure, and
// nothing in src is parsed by anyone but goldmark.
func renderRich(src string) richMessage {
	source := []byte(src)
	doc := md.Parser().Parse(text.NewReader(source))
	return richMessage{Blocks: (&richWriter{src: source}).blocks(doc)}
}

// structured reports whether the Markdown has what an ordinary message cannot
// show: a table, or at least two headings, at any depth (R-DLV-11).
func structured(src string) bool {
	source := []byte(src)
	doc := md.Parser().Parse(text.NewReader(source))
	headings, table := 0, false
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch n.(type) {
		case *ast.Heading:
			headings++
		case *east.Table:
			table = true
		}
		return ast.WalkContinue, nil
	})
	return table || headings >= minRichHeadings
}

type richWriter struct {
	src []byte
}

func (w *richWriter) blocks(n ast.Node) []richBlock {
	var out []richBlock
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, w.block(c)...)
	}
	return out
}

func (w *richWriter) block(n ast.Node) []richBlock {
	switch n := n.(type) {
	case *ast.Paragraph, *ast.TextBlock:
		if tree, ok := treeSource(w.src, n); ok {
			return []richBlock{{Type: richPre, Text: tree}}
		}
		return []richBlock{{Type: richParagraph, Text: w.inlines(n)}}
	case *ast.Heading:
		return []richBlock{{Type: richHeading, Text: w.inlines(n), Size: n.Level}}
	case *ast.FencedCodeBlock:
		return []richBlock{{Type: richPre, Text: w.lines(n), Language: string(n.Language(w.src))}}
	case *ast.CodeBlock:
		return []richBlock{{Type: richPre, Text: w.lines(n)}}
	case *ast.Blockquote:
		return []richBlock{w.quote(n)}
	case *ast.List:
		return []richBlock{w.list(n)}
	case *ast.ThematicBreak:
		return []richBlock{{Type: richDivider}}
	case *ast.HTMLBlock:
		return []richBlock{{Type: richParagraph, Text: w.lines(n)}}
	case *east.Table:
		return []richBlock{w.table(n)}
	default:
		return w.blocks(n)
	}
}

func (w *richWriter) lines(n ast.Node) string {
	return strings.TrimRight((&writer{src: w.src}).lines(n), "\n")
}

// quote is a blockquote; a long one that holds only paragraphs is collapsed,
// since a collapsed quote takes a single rich text and no blocks.
func (w *richWriter) quote(n *ast.Blockquote) richBlock {
	blocks := w.blocks(n)
	if text, ok := oneText(blocks); ok && (richSizeOf(blocks).Chars > expandableQuote) {
		return richBlock{Type: richExpandable, Text: text}
	}
	return richBlock{Type: richQuote, Blocks: blocks}
}

// oneText joins paragraphs into one rich text, false for anything else.
func oneText(blocks []richBlock) (any, bool) {
	parts := make([]any, 0, 2*len(blocks))
	for i, b := range blocks {
		if b.Type != richParagraph {
			return nil, false
		}
		if i > 0 {
			parts = append(parts, "\n")
		}
		parts = append(parts, b.Text)
	}
	return simplify(parts), true
}

func (w *richWriter) list(n *ast.List) richBlock {
	num := n.Start
	var items []richItem
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		item := richItem{Blocks: w.blocks(c)}
		if n.IsOrdered() {
			item.Value, item.Type = num, orderedLabel
			num++
		}
		items = append(items, item)
	}
	return richBlock{Type: richList, Items: items}
}

// table is a native table; one wider than Telegram allows stays a preformatted
// block, laid out as in an ordinary message. A short row is padded, so that
// columns stay in line.
func (w *richWriter) table(t *east.Table) richBlock {
	columns := 0
	if head := t.FirstChild(); head != nil {
		columns = head.ChildCount()
	}
	if columns > maxRichColumns {
		return richBlock{Type: richPre, Text: (&writer{src: w.src}).table(t)}
	}
	var rows [][]richCell
	for r := t.FirstChild(); r != nil; r = r.NextSibling() {
		_, header := r.(*east.TableHeader)
		rows = append(rows, w.row(r, t.Alignments, columns, header))
	}
	return richBlock{Type: richTable, Cells: rows, IsBordered: true}
}

func (w *richWriter) row(r ast.Node, aligns []east.Alignment, columns int, header bool) []richCell {
	row := make([]richCell, 0, columns)
	c := r.FirstChild()
	for i := range columns {
		cell := richCell{Text: emptyCell, IsHeader: header, Align: cellAlign(aligns, i), VAlign: "top"}
		if c != nil {
			if t := w.inlines(c); t != "" {
				cell.Text = t
			}
			c = c.NextSibling()
		}
		row = append(row, cell)
	}
	return row
}

func cellAlign(aligns []east.Alignment, i int) string {
	if i < len(aligns) {
		switch aligns[i] {
		case east.AlignCenter:
			return "center"
		case east.AlignRight:
			return "right"
		case east.AlignLeft, east.AlignNone:
		}
	}
	return "left"
}

// inlines is the rich text of n's inline children.
func (w *richWriter) inlines(n ast.Node) any {
	var parts []any
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		parts = append(parts, w.inline(c)...)
	}
	return simplify(parts)
}

func (w *richWriter) inline(n ast.Node) []any {
	switch n := n.(type) {
	case *ast.Text:
		out := []any{textValue(w.src, n)}
		if n.SoftLineBreak() || n.HardLineBreak() {
			out = append(out, "\n")
		}
		return out
	case *ast.String:
		return []any{string(n.Value)}
	case *ast.Emphasis:
		kind := richItalic
		if n.Level >= 2 {
			kind = richBold
		}
		return []any{richSpan{Type: kind, Text: w.inlines(n)}}
	case *east.Strikethrough:
		return []any{richSpan{Type: richStrike, Text: w.inlines(n)}}
	case *ast.CodeSpan:
		return []any{richSpan{Type: richCode, Text: codeValue(w.src, n)}}
	case *ast.Link:
		return []any{link(w.inlines(n), string(n.Destination))}
	case *ast.Image:
		return []any{image(w.inlines(n), string(n.Destination))}
	case *ast.AutoLink:
		return []any{string(n.URL(w.src))} // Telegram links URLs by itself
	case *ast.RawHTML:
		var b strings.Builder
		for i := range n.Segments.Len() {
			seg := n.Segments.At(i)
			b.Write(seg.Value(w.src))
		}
		return []any{b.String()}
	default:
		var parts []any
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			parts = append(parts, w.inline(c)...)
		}
		return parts
	}
}

// link is a link for an http or https address, and only its text for any other,
// such as tg:// (ADR-0054).
func link(text any, dest string) any {
	if !webAddress(dest) {
		return text
	}
	return richSpan{Type: richURL, Text: text, URL: dest}
}

// image is never a media block, whose address would be fetched with no press: it
// is a link, showing its address when it has no text of its own.
func image(text any, dest string) any {
	if text == "" && webAddress(dest) {
		text = dest
	}
	return link(text, dest)
}

func webAddress(dest string) bool {
	u, err := url.Parse(dest)
	if err != nil || u.Host == "" {
		return false
	}
	return strings.EqualFold(u.Scheme, "http") || strings.EqualFold(u.Scheme, "https")
}

// simplify joins neighbouring strings, and unwraps a list of one.
func simplify(parts []any) any {
	var out []any
	for _, p := range parts {
		s, ok := p.(string)
		if !ok {
			out = append(out, p)
			continue
		}
		if s == "" {
			continue
		}
		if n := len(out); n > 0 {
			if prev, isString := out[n-1].(string); isString {
				out[n-1] = prev + s
				continue
			}
		}
		out = append(out, s)
	}
	switch len(out) {
	case 0:
		return ""
	case 1:
		return out[0]
	default:
		return out
	}
}

// richSize is how much of Telegram's limits a message uses.
type richSize struct {
	Chars, Blocks, Depth int
}

func (s richSize) fits() bool {
	return s.Chars <= maxRichChars && s.Blocks <= maxRichBlocks && s.Depth <= maxRichDepth
}

func (m richMessage) size() richSize { return richSizeOf(m.Blocks) }

func richSizeOf(blocks []richBlock) richSize {
	var s richSize
	for _, b := range blocks {
		s.Blocks++
		n, d := textSize(b.Text)
		s.Chars += n
		deepest := d
		inner := []richSize{richSizeOf(b.Blocks)}
		for _, item := range b.Items {
			s.Blocks++
			inner = append(inner, richSizeOf(item.Blocks))
		}
		for _, row := range b.Cells {
			s.Blocks++
			for _, c := range row {
				n, d := textSize(c.Text)
				s.Chars += n
				deepest = max(deepest, d)
			}
		}
		for _, i := range inner {
			s.Chars += i.Chars
			s.Blocks += i.Blocks
			deepest = max(deepest, i.Depth)
		}
		s.Depth = max(s.Depth, deepest+1)
	}
	return s
}

// textSize is the characters of rich text and how deep its formatting goes.
func textSize(t any) (chars, depth int) {
	switch t := t.(type) {
	case string:
		return utf8.RuneCountInString(t), 0
	case richSpan:
		n, d := textSize(t.Text)
		return n, d + 1
	case []any:
		for _, p := range t {
			n, d := textSize(p)
			chars += n
			depth = max(depth, d)
		}
	}
	return chars, depth
}
