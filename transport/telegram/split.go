package telegram

import (
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/igorrius/ganglion/event"
)

// maxTextUnits is the longest message Telegram accepts, in UTF-16 code
// units.
const maxTextUnits = 4096

// Parts implements transport.Parter (ADR-0025). A message whose rendered
// text fits maxTextUnits comes back unchanged. A longer one is split
// between Markdown blocks (paragraphs, lists, quotes, code blocks), packing
// whole blocks while the rendered part fits. A code block that alone is too
// long becomes several code blocks; any other block that alone is too long
// is cut by the M2 rule (a newline or space in the final quarter, else
// hard), which may lose formatting at the cut but never breaks a message.
// Part i gets IdemKey + ":p<i>"; only the first part keeps ReplyTo. The
// result depends only on msg, so a resumed sender gets the same parts.
func (t *Transport) Parts(msg event.Outgoing) []event.Outgoing {
	if renderedUnits(msg.Text) <= maxTextUnits {
		return []event.Outgoing{msg}
	}
	var parts []event.Outgoing
	for i, chunk := range splitMarkdown(msg.Text) {
		part := msg
		part.IdemKey = msg.IdemKey + ":p" + strconv.Itoa(i)
		part.Text = chunk
		if i > 0 {
			part.ReplyTo = ""
		}
		parts = append(parts, part)
	}
	return parts
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
		runes := []rune(b)
		for start := 0; start < len(runes); {
			cut := nextCut(runes, start)
			blocks = append(blocks, string(runes[start:cut]))
			start = cut
		}
	}
	var chunks []string
	cur := ""
	for _, b := range blocks {
		if cur == "" {
			cur = b
			continue
		}
		if cand := cur + "\n\n" + b; renderedUnits(cand) <= maxTextUnits {
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
