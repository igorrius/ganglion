package telegram

import (
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/igorrius/ganglion/event"
)

// maxTextUnits is the longest message Telegram accepts, in UTF-16 code
// units; maxCaptionUnits the longest caption of a picture or a document.
const (
	maxTextUnits    = 4096
	maxCaptionUnits = 1024
)

// Parts implements transport.Parter (ADR-0025). A message that goes rich has the
// limits of a rich message instead (R-DLV-11). Any other message whose rendered
// text fits maxTextUnits comes back unchanged. A longer one is split
// between Markdown blocks (paragraphs, lists, quotes, code blocks), packing
// whole blocks while the rendered part fits. A code block that alone is too
// long becomes several code blocks; any other block that alone is too long
// is cut by the M2 rule (a newline or space in the final quarter, else
// hard), which may lose formatting at the cut but never breaks a message.
// Part i gets IdemKey + ":p<i>"; only the first part keeps ReplyTo and
// only the last keeps the buttons. An edit is never split: it changes one
// message. An attachment whose text does not fit a caption goes first
// without one, and the text follows as the next parts (R-DLV-9). The
// result depends only on msg, so a resumed sender gets the same parts.
func (t *Transport) Parts(msg event.Outgoing) []event.Outgoing {
	if msg.Edit != "" || msg.Ack != "" {
		return []event.Outgoing{msg}
	}
	if msg.Attach != nil {
		return attachmentParts(msg)
	}
	if t.richShape(msg) && structured(msg.Text) {
		return richParts(msg)
	}
	if renderedUnits(msg.Text) <= maxTextUnits {
		return []event.Outgoing{msg}
	}
	return textParts(msg, splitMarkdown(msg.Text), 0)
}

// richParts keeps a rich message whole when it fits Telegram's limits for one.
func richParts(msg event.Outgoing) []event.Outgoing {
	if richFits(msg.Text) {
		return []event.Outgoing{msg}
	}
	return textParts(msg, splitRich(msg.Text), 0)
}

func richFits(src string) bool { return renderRich(src).size().fits() }

// splitRich packs the blocks of src into chunks that fit a rich message. A table
// too long for one is cut between rows, and a code block as in splitMarkdown. Any
// other block too long for one is cut by the ordinary rule, which may lose its
// formatting there but never breaks a message.
func splitRich(src string) []string {
	var blocks []string
	for _, b := range mdBlocks(src) {
		switch {
		case richFits(b):
			blocks = append(blocks, b)
		case splitsAsTable(b):
			blocks = append(blocks, splitTable(b, richFits)...)
		case fenceOpen(firstLine(b)) != "":
			blocks = append(blocks, splitFence(b)...)
		default:
			blocks = append(blocks, cutBlock(b)...)
		}
	}
	return pack(blocks, richFits)
}

// tableRule is a table's delimiter line.
var tableRule = regexp.MustCompile(`^\s*\|?\s*:?-+:?\s*(\|\s*:?-+:?\s*)*\|?\s*$`)

// tableHead returns the index of the delimiter line of the table in lines, or
// -1. A rule alone on the first line has no header above it, so it is no table.
func tableHead(lines []string) int {
	for i := 1; i < len(lines); i++ {
		// A rule with no bar is a thematic break or makes the line above a heading.
		if strings.Contains(lines[i], "|") && tableRule.MatchString(lines[i]) {
			return i
		}
	}
	return -1
}

// splitsAsTable reports whether block's lines can be cut as a table's rows. A
// table in the parsed tree is what counts: inside a code block the same lines are
// code.
func splitsAsTable(block string) bool {
	return hasTable(block) && tableHead(strings.Split(block, "\n")) > 0
}

// splitTable cuts a table's source between rows, with the header and the
// delimiter in each chunk, so that every chunk is a table of its own and fits
// unless a single row is too long for that.
func splitTable(block string, fits func(string) bool) []string {
	lines := strings.Split(block, "\n")
	rule := tableHead(lines)
	head := lines[rule-1] + "\n" + lines[rule]
	cur := strings.Join(lines[:rule+1], "\n")
	var out []string
	for i, row := range lines[rule+1:] {
		if cand := cur + "\n" + row; i == 0 || fits(cand) {
			cur = cand
			continue
		}
		out = append(out, cur)
		cur = head + "\n" + row
	}
	return append(out, cur)
}

// attachmentParts keeps an attachment whole when its text fits a caption
// (or it has buttons, which Send refuses); otherwise the attachment is
// part 0 with no caption and the text the parts after it.
func attachmentParts(msg event.Outgoing) []event.Outgoing {
	if len(msg.Buttons) > 0 || renderedUnits(msg.Text) <= maxCaptionUnits {
		return []event.Outgoing{msg}
	}
	first := msg
	first.IdemKey, first.Text = msg.IdemKey+":p0", ""
	text := msg
	text.Attach, text.ReplyTo = nil, ""
	chunks := []string{msg.Text}
	if renderedUnits(msg.Text) > maxTextUnits {
		chunks = splitMarkdown(msg.Text)
	}
	return append([]event.Outgoing{first}, textParts(text, chunks, 1)...)
}

// textParts makes one part of msg per chunk, numbered from first: only
// part 0 keeps ReplyTo and only the last keeps the buttons.
func textParts(msg event.Outgoing, chunks []string, first int) []event.Outgoing {
	parts := make([]event.Outgoing, 0, len(chunks))
	for i, chunk := range chunks {
		part := msg
		part.IdemKey = msg.IdemKey + ":p" + strconv.Itoa(first+i)
		part.Text = chunk
		if first+i > 0 {
			part.ReplyTo = ""
		}
		if i < len(chunks)-1 {
			part.Buttons = nil
		}
		parts = append(parts, part)
	}
	return parts
}

// editUnits is the length of an edit as Telegram counts it: the text, and a
// folded question on its own line.
func editUnits(msg event.Outgoing) int {
	n := renderedUnits(msg.Text)
	if msg.Folded != "" {
		n += 1 + renderedUnits(msg.Folded)
	}
	return n
}

func renderedUnits(md string) int {
	text, _ := render(md)
	return textUnits([]rune(text))
}

// splitMarkdown packs the blocks of src into chunks that render within
// the limit.
func splitMarkdown(src string) []string {
	var blocks []string
	for _, b := range mdBlocks(src) {
		if renderedUnits(b) <= maxTextUnits {
			blocks = append(blocks, b)
			continue
		}
		if fenceOpen(firstLine(b)) != "" {
			blocks = append(blocks, splitFence(b)...)
			continue
		}
		// A table's size is its widest cell times its rows: its source may be short.
		if splitsAsTable(b) {
			blocks = append(blocks, splitTable(b, fitsMessage)...)
			continue
		}
		blocks = append(blocks, cutBlock(b)...)
	}
	return pack(blocks, fitsMessage)
}

func fitsMessage(chunk string) bool { return renderedUnits(chunk) <= maxTextUnits }

// cutBlock cuts a block by the M2 rule.
func cutBlock(b string) []string {
	var out []string
	runes := []rune(b)
	for start := 0; start < len(runes); {
		cut := nextCut(runes, start)
		out = append(out, string(runes[start:cut]))
		start = cut
	}
	return out
}

// pack joins neighbouring blocks into chunks while fits holds.
func pack(blocks []string, fits func(string) bool) []string {
	var chunks []string
	cur := ""
	for _, b := range blocks {
		if cur == "" {
			cur = b
			continue
		}
		if cand := cur + "\n\n" + b; fits(cand) {
			cur = cand
			continue
		}
		chunks = append(chunks, cur)
		cur = b
	}
	if cur != "" {
		chunks = append(chunks, cur)
	}
	return chunks
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// fenceOpen returns the fence marker (``` or ~~~, possibly longer) that
// opens a code block on this line, or "".
func fenceOpen(line string) string {
	t := strings.TrimLeft(line, " ")
	for _, ch := range []string{"`", "~"} {
		n := 0
		for n < len(t) && t[n:n+1] == ch {
			n++
		}
		if n >= 3 {
			return t[:n]
		}
	}
	return ""
}

// mdBlocks splits src at blank lines outside code fences.
func mdBlocks(src string) []string {
	var blocks, cur []string
	fence := ""
	flush := func() {
		if len(cur) > 0 {
			blocks = append(blocks, strings.Join(cur, "\n"))
			cur = nil
		}
	}
	for line := range strings.SplitSeq(src, "\n") {
		switch {
		case fence != "":
			cur = append(cur, line)
			if strings.HasPrefix(strings.TrimLeft(line, " "), fence) && strings.TrimSpace(strings.TrimLeft(strings.TrimLeft(line, " "), fence[:1])) == "" {
				fence = ""
			}
		case fenceOpen(line) != "":
			fence = fenceOpen(line)
			cur = append(cur, line)
		case strings.TrimSpace(line) == "":
			flush()
		default:
			cur = append(cur, line)
		}
	}
	flush()
	return blocks
}

// splitFence cuts an oversized fenced code block into several fenced
// blocks with the same opening line.
func splitFence(b string) []string {
	lines := strings.Split(b, "\n")
	open := lines[0]
	fence := fenceOpen(open)
	body := lines[1:]
	if n := len(body); n > 0 && strings.HasPrefix(strings.TrimSpace(body[n-1]), fence) {
		body = body[:n-1]
	}
	wrap := func(ls []string) string { return open + "\n" + strings.Join(ls, "\n") + "\n" + fence }
	var out []string
	var cur []string
	const room = maxTextUnits - 16
	for _, l := range body {
		for r := []rune(l); textUnits(r) > room; r = []rune(l) { // a single line longer than a message
			cut, u := 0, 0
			for cut < len(r) && u+runeUnits(r[cut]) <= room {
				u += runeUnits(r[cut])
				cut++
			}
			if len(cur) > 0 {
				out, cur = append(out, wrap(cur)), nil
			}
			out = append(out, wrap([]string{string(r[:cut])}))
			l = string(r[cut:])
		}
		if len(cur) > 0 && renderedUnits(wrap(append(cur, l))) > maxTextUnits {
			out, cur = append(out, wrap(cur)), nil
		}
		cur = append(cur, l)
	}
	if len(cur) > 0 {
		out = append(out, wrap(cur))
	}
	return out
}

// nextCut returns the end of the chunk that starts at start.
func nextCut(runes []rune, start int) int {
	// prefix[k] is the size in units of runes[start:start+k].
	prefix := []int{0}
	end := start
	for end < len(runes) {
		u := prefix[len(prefix)-1] + runeUnits(runes[end])
		if u > maxTextUnits {
			break
		}
		prefix = append(prefix, u)
		end++
	}
	if end == len(runes) {
		return end
	}
	const quarter = maxTextUnits * 3 / 4
	for _, sep := range []rune{'\n', ' '} {
		for k := end; k > start && prefix[k-start] >= quarter; k-- {
			if runes[k-1] == sep {
				return k
			}
		}
	}
	return end
}

func runeUnits(r rune) int {
	if n := utf16.RuneLen(r); n > 0 {
		return n
	}
	return 1 // an invalid rune is sent as U+FFFD
}

func textUnits(runes []rune) int {
	n := 0
	for _, r := range runes {
		n += runeUnits(r)
	}
	return n
}
