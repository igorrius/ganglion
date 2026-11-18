package mcpclient

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"github.com/igorrius/ganglion/mcp"
)

// maxListenEvent is the biggest event of the GET stream: the stream's total
// is not bounded in time, so only each event is (R-MCP-1).
const maxListenEvent = 64 << 10

// streamSlack is what a stream may carry beyond its answer's limit: event
// lines, notifications.
const streamSlack = 64 << 10

// capReader reads at most left bytes of r and then fails with ErrTooLarge,
// so a stream that never ends is cut, not buffered.
type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	if c.left <= 0 && err == nil {
		err = ErrTooLarge
	}
	return n, err
}

// sseEvent is one server-sent event.
type sseEvent struct {
	name string
	id   string
	data []byte
}

// eventReader reads server-sent events from a bounded line reader.
type eventReader struct {
	lines   *mcp.LineReader
	maxData int
}

// newEventReader reads events whose data is at most maxData bytes, from a
// body already capped by the caller.
func newEventReader(r io.Reader, maxData int) *eventReader {
	return &eventReader{lines: mcp.NewLineReader(r, maxData+len("data: ")+2), maxData: maxData}
}

// next returns the next event: "data:" lines joined with a newline,
// "event:" and "id:", comments and other fields ignored, ended by a blank
// line. An event without data is skipped. The end of the stream ends an
// event that has data. A line or an event over the limit is ErrTooLarge.
func (r *eventReader) next() (sseEvent, error) {
	var ev sseEvent
	has := false
	for {
		line, err := r.lines.Next()
		switch {
		case errors.Is(err, mcp.ErrLineTooLong):
			return sseEvent{}, fmt.Errorf("%w: an event line is over the limit", ErrTooLarge)
		case errors.Is(err, io.EOF) && has:
			return ev, nil
		case err != nil:
			return sseEvent{}, err
		}
		if len(line) == 0 {
			if has {
				return ev, nil
			}
			ev = sseEvent{}
			continue
		}
		if line[0] == ':' {
			continue
		}
		if err := ev.field(line, &has, r.maxData); err != nil {
			return sseEvent{}, err
		}
	}
}

// field takes one line of an event.
func (e *sseEvent) field(line []byte, has *bool, limit int) error {
	name, value, _ := bytes.Cut(line, []byte(":"))
	value = bytes.TrimPrefix(value, []byte(" "))
	switch string(name) {
	case "data":
		if *has {
			e.data = append(e.data, '\n')
		}
		e.data = append(e.data, value...)
		*has = true
		if len(e.data) > limit {
			return fmt.Errorf("%w: an event is over the limit", ErrTooLarge)
		}
	case "event":
		e.name = string(bytes.TrimSpace(value))
	case "id":
		e.id = string(bytes.TrimSpace(value))
	}
	return nil
}
