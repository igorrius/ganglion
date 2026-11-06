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

// treeSource returns the lines of a paragraph that holds a tree, as they were
// written, and false for any other paragraph. goldmark trims the start of a
// paragraph's lines, which is where a tree keeps its levels, so each line is read
// back from the source. What the first line's start shows of its container, such
// as a list item's indentation, comes off every line.
func treeSource(src []byte, n ast.Node) (string, bool) {
	segs := n.Lines()
	if segs.Len() == 0 {
		return "", false
	}
	base := segs.At(0).Start - (bytes.LastIndexByte(src[:segs.At(0).Start], '\n') + 1)
	lines := make([]string, 0, segs.Len())
	tree := false
	for i := range segs.Len() {
		seg := segs.At(i)
		line := strings.TrimRight(string(src[indentStart(src, seg.Start, base):seg.Stop]), "\r\n")
		tree = tree || isTreeLine(line)
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), tree
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
