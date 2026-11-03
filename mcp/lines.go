package mcp

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// MaxLine is the longest JSON-RPC line the server and the relay read,
// newline included: 8 MiB. A longer line is a protocol error; neither
// side buffers it.
const MaxLine = 8 << 20

// ErrLineTooLong is a line longer than the reader's limit. The stream
// cannot be resynchronised after it, so whoever reads it stops.
var ErrLineTooLong = errors.New("mcp: line too long")

// lineReader reads newline-terminated lines of at most max bytes.
type lineReader struct {
	r   *bufio.Reader
	max int
}

func newLineReader(r io.Reader, limit int) *lineReader {
	return &lineReader{r: bufio.NewReaderSize(r, min(limit, 64<<10)), max: limit}
}

// next returns the next line without its line ending. A last line
// without a newline is returned with a nil error; io.EOF comes after
// it. A line longer than max is ErrLineTooLong, read no further than
// max bytes.
func (l *lineReader) next() ([]byte, error) {
	var line []byte
	for {
		chunk, err := l.r.ReadSlice('\n')
		if len(line)+len(chunk) > l.max {
			return nil, ErrLineTooLong
		}
		line = append(line, chunk...)
		switch {
		case err == nil:
			return bytes.TrimRight(line, "\r\n"), nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF) && len(line) > 0:
			return bytes.TrimRight(line, "\r"), nil
		default:
			return nil, err
		}
	}
}
