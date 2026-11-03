package diag

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/redact"
)

// currentLog is the file being written; rotatedLog the files renamed aside
// (package logfile).
const currentLog = "ganglion.log"

var rotatedLog = regexp.MustCompile(`^ganglion-\d{8}T\d{6}\.\d{3}Z(?:-\d+)?\.log$`)

// maxLogLine is the longest line read; a longer one is skipped.
const maxLogLine = 1 << 20

// logFiles are the log files, newest first.
func (d *Diag) logFiles() ([]string, error) {
	es, err := os.ReadDir(d.logDir)
	if err != nil {
		return nil, err
	}
	var rotated []string
	for _, e := range es {
		if e.Type().IsRegular() && rotatedLog.MatchString(e.Name()) {
			rotated = append(rotated, e.Name())
		}
	}
	slices.Sort(rotated) // the names sort by time
	slices.Reverse(rotated)
	out := []string{filepath.Join(d.logDir, currentLog)}
	for _, n := range rotated {
		out = append(out, filepath.Join(d.logDir, n))
	}
	return out, nil
}

// logQuery is what logs.search looks for.
type logQuery struct {
	text  string // lower case; "" matches every line
	level slog.Level
	since time.Time
}

func (q logQuery) match(line []byte) bool {
	var rec struct {
		Time  time.Time `json:"time"`
		Level string    `json:"level"`
	}
	if json.Unmarshal(line, &rec) != nil {
		return false // not one of ours
	}
	var l slog.Level
	if l.UnmarshalText([]byte(rec.Level)) != nil || l < q.level || rec.Time.Before(q.since) {
		return false
	}
	return q.text == "" || strings.Contains(strings.ToLower(string(line)), q.text)
}

func (d *Diag) logsSearch(_ context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		window
		Query string `json:"query"`
		Level string `json:"level"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	since, limit, err := d.bounds(a.window)
	if err != nil {
		return nil, err
	}
	q := logQuery{text: strings.ToLower(a.Query), level: slog.LevelDebug, since: since}
	if a.Level != "" {
		if err := q.level.UnmarshalText([]byte(a.Level)); err != nil {
			return nil, fmt.Errorf("diag: level %q: want debug, info, warn or error", a.Level)
		}
	}
	if d.logDir == "" {
		return nil, errors.New("diag: logs are not on disk here (--log-dir off)")
	}
	files, err := d.logFiles()
	if err != nil {
		return nil, fmt.Errorf("diag: logs: %w", err)
	}
	var (
		lines   []json.RawMessage
		skipped int
	)
	for _, f := range files {
		got, n, err := lastMatches(f, q, limit-len(lines))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("diag: logs: %w", err)
		}
		lines, skipped = append(lines, got...), skipped+n
		if len(lines) >= limit {
			break
		}
	}
	out := map[string]any{"lines": lines}
	if skipped > 0 {
		out["skipped_long_lines"] = skipped
	}
	return out, nil
}

// lastMatches reads one file and keeps its last n matching lines, newest
// first, in a ring of n: memory stays bounded whatever the file's size. A
// line longer than maxLogLine is skipped and counted, and the reading
// goes on after it. Lines are redacted once more: a log written before a
// pattern existed may still hold a secret.
func lastMatches(path string, q logQuery, n int) ([]json.RawMessage, int, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = f.Close() }()
	ring := make([]json.RawMessage, 0, n)
	next, skipped := 0, 0
	r := bufio.NewReaderSize(f, 64<<10)
	for {
		line, long, err := readLine(r)
		if long {
			skipped++
		} else if len(line) > 0 && q.match(line) {
			cp := json.RawMessage(redactLine(line))
			if len(ring) < n {
				ring = append(ring, cp)
			} else {
				ring[next] = cp
			}
			next = (next + 1) % n
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, err
		}
	}
	ordered := ring // not full: oldest first already
	if len(ring) == n {
		ordered = append(slices.Clone(ring[next:]), ring[:next]...)
	}
	slices.Reverse(ordered)
	return ordered, skipped, nil
}

// readLine reads one line without its newline; long reports a line over
// maxLogLine, read to its end and dropped.
func readLine(r *bufio.Reader) (line []byte, long bool, err error) {
	var buf []byte
	for {
		chunk, err := r.ReadSlice('\n')
		if !long {
			if len(buf)+len(chunk) > maxLogLine {
				long, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimRight(buf, "\r\n"), long, err
	}
}

// redactLine redacts a JSON log line's string values, or the line as
// text when it is not JSON.
func redactLine(line []byte) []byte {
	out := redact.String(string(line))
	if !json.Valid([]byte(out)) {
		raw, _ := json.Marshal(out)
		return raw
	}
	return []byte(out)
}
