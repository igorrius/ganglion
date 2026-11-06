package telegram

import (
	"bytes"
	"strings"

	"github.com/yuin/goldmark/ast"
)

// isTreeLine reports whether line is a branch of a tree drawn with symbols:
// after any spaces and vertical bars it starts with a branch. The symbol must
// come first, so that prose and a plan's steps, which start with a mark, stay
// text however they use it.
func isTreeLine(line string) bool {
	rest := strings.TrimLeft(line, " \t│")
	return strings.HasPrefix(rest, "├") || strings.HasPrefix(rest, "└")
}

// noBreakSpace is a space that Markdown and Telegram keep at the start of a line.
const noBreakSpace = '\u00a0'

// treeLeads maps the start of each line of a paragraph that holds a tree to the
// indentation that line had, and is nil for any other paragraph. goldmark trims
// the start of a paragraph's lines, which is where a tree keeps its levels, so each
// is read back from the source. What the first line's start shows of its container,
// such as a list item's indentation, comes off every line. The tree stays ordinary
// text, so that its links and formatting stay too.
func treeLeads(src []byte, n ast.Node) map[int]string {
	segs := n.Lines()
	if segs.Len() == 0 {
		return nil
	}
	base := segs.At(0).Start - (bytes.LastIndexByte(src[:segs.At(0).Start], '\n') + 1)
	leads := make(map[int]string, segs.Len())
	tree := false
	for i := range segs.Len() {
		seg := segs.At(i)
		tree = tree || isTreeLine(string(src[seg.Start:seg.Stop]))
		leads[seg.Start] = strings.Repeat(string(noBreakSpace), seg.Start-indentStart(src, seg.Start, base))
	}
	if !tree {
		return nil
	}
	return leads
}

// indentStart walks back from start over the spaces before it, keeping what
// lies beyond the container's base indentation.
func indentStart(src []byte, start, base int) int {
	at := start
	for at > 0 && (src[at-1] == ' ' || src[at-1] == '\t') {
		at--
	}
	return min(at+base, start)
}

// treeLine is the text of a tree's line: its indentation, then its branches with
// the spaces between them kept.
func treeLine(lead, text string) string {
	rest := strings.TrimLeft(text, " \t│")
	prefix := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' {
			return noBreakSpace
		}
		return r
	}, text[:len(text)-len(rest)])
	return lead + prefix + rest
}
