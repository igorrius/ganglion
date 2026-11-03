package diag

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
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
	var lines []json.RawMessage
	for _, f := range files {
		got, err := lastMatches(f, q, limit-len(lines))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("diag: logs: %w", err)
		}
		lines = append(lines, got...)
		if len(lines) >= limit {
			break
		}
	}
	return map[string]any{"lines": lines}, nil
}

// lastMatches reads one file and keeps its last n matching lines, newest
// first, in a ring of n: memory stays bounded whatever the file's size.
func lastMatches(path string, q logQuery, n int) ([]json.RawMessage, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	ring := make([]json.RawMessage, 0, n)
	next := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), maxLogLine)
	for sc.Scan() {
		line := sc.Bytes()
		if !q.match(line) {
			continue
		}
		cp := json.RawMessage(slices.Clone(line))
		if len(ring) < n {
			ring = append(ring, cp)
		} else {
			ring[next] = cp
		}
		next = (next + 1) % n
	}
	if err := sc.Err(); err != nil && !errors.Is(err, bufio.ErrTooLong) {
		return nil, err
	}
	ordered := ring // not full: oldest first already
	if len(ring) == n {
		ordered = append(slices.Clone(ring[next:]), ring[:next]...)
	}
	slices.Reverse(ordered)
	return ordered, nil
}
