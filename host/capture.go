package host

import (
	"fmt"
	"sync"
)

// capture keeps the first head and the last tail bytes of a command's
// output, and counts all of it: enough for the model to see how it began
// and how it ended, never a flood (R-CMD-1). Stdout and stderr share it, so
// their lines stay in the order they came.
type capture struct {
	mu         sync.Mutex
	head, tail []byte
	maxH, maxT int
	total      int64
}

func newCapture(head, tail int) *capture { return &capture{maxH: head, maxT: tail} }

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

// text is the kept output, with a mark where the middle was left out.
func (c *capture) text() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	tail := c.tail
	if len(tail) > c.maxT {
		tail = tail[len(tail)-c.maxT:]
	}
	kept := int64(len(c.head) + len(tail))
	if kept == c.total {
		return string(c.head) + string(tail), false
	}
	return fmt.Sprintf("%s\n… %d bytes left out …\n%s", c.head, c.total-kept, tail), true
}

// size is how many bytes the command wrote in all.
func (c *capture) size() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}
