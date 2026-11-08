package host

import (
	"fmt"
	"strings"
	"sync"
)

// edge is how many bytes capture keeps past each cut, so a secret the cut
// goes through is hidden whole before the cut: a secret is shorter than this.
const edge = 256

// capture keeps the first head and the last tail bytes of a command's
// output, and counts all of it: enough for the model to see how it began
// and how it ended, never a flood (R-CMD-1). Stdout and stderr share it, so
// their lines stay in the order they came. It keeps edge bytes more on each
// side, which text hides with the rest and then cuts off.
type capture struct {
	mu           sync.Mutex
	head, tail   []byte
	keepH, keepT int // what the model reads of each side
	maxH, maxT   int // what is kept: the above and an edge
	total        int64
}

func newCapture(head, tail int) *capture {
	return &capture{keepH: head, keepT: tail, maxH: head + edge, maxT: tail + edge}
}

// Write implements io.Writer; it never fails, so the command never blocks.
func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total += int64(len(p))
	rest := p
	if room := c.maxH - len(c.head); room > 0 {
		n := min(room, len(rest))
		c.head, rest = append(c.head, rest[:n]...), rest[n:]
	}
	if len(rest) > 0 {
		c.tail = append(c.tail, rest...)
		if over := len(c.tail) - c.maxT; over > c.maxT { // trim now and then, not on every write
			c.tail = append(c.tail[:0], c.tail[over:]...)
		}
	}
	return len(p), nil
}

// text is the kept output, valid UTF-8 and hidden, with a mark where the
// middle was left out. Each side is hidden with its edge before it is cut,
// so a secret across a cut is hidden whole; a cut through a hidden mark
// shows only the mark's part. Hiding shrinks or grows the text, so where
// the output went on past a side's edge, edge bytes of the hidden side are
// dropped at that end before the cut: they may hold a secret the edge's end
// cut through, which hide could not match (a secret is shorter than edge).
// A side may then show fewer than its bytes. The count in the mark is the
// output outside its first head and last tail bytes: exact when nothing was
// hidden, else what those sides stand for, not the bytes of the text shown.
func (c *capture) text(hide func(string) string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.total <= int64(c.keepH+c.keepT) {
		return hide(strings.ToValidUTF8(string(c.head)+string(c.tail), "")), false
	}
	head := hide(strings.ToValidUTF8(string(c.head), ""))
	end := len(head)
	if c.total > int64(len(c.head)) { // the output went on past the head's edge
		end -= edge
	}
	head = strings.ToValidUTF8(head[:max(0, min(c.keepH, end))], "")
	raw, cut := c.tailEdge()
	tail := hide(strings.ToValidUTF8(string(raw), ""))
	from := len(tail) - c.keepT
	if cut { // the tail's edge began inside the output
		from = max(from, edge)
	}
	tail = strings.ToValidUTF8(tail[min(len(tail), max(0, from)):], "")
	return fmt.Sprintf("%s\n… %d bytes left out …\n%s", head, c.total-int64(c.keepH+c.keepT), tail), true
}

// tailEdge is the last maxT bytes of the output, and whether output came
// before them. While nothing was dropped they may begin in the head.
func (c *capture) tailEdge() ([]byte, bool) {
	all := c.tail
	if int64(len(c.head)+len(c.tail)) == c.total {
		all = append(append([]byte(nil), c.head...), c.tail...)
	}
	start := max(0, len(all)-c.maxT)
	return all[start:], c.total > int64(len(all)-start)
}

// size is how many bytes the command wrote in all.
func (c *capture) size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}
