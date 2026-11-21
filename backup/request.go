package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
)

// requestName is the file, in the data directory, with which a command asks
// the running node for a backup (R-BAK-1).
const requestName = "backup-request"

// How often the node looks for a request, how often the asking command looks
// for the answer, and how long it waits.
const (
	// RequestPoll is how often the daily component looks for a request.
	RequestPoll = 5 * time.Second
	// AnswerPoll is how often Ask looks for the answer.
	AnswerPoll = time.Second
	// AnswerWait is how long Ask waits for the answer.
	AnswerWait = 10 * time.Minute
)

// requestIDLen is the length of a request id (event.RandomID).
const requestIDLen = 26

var (
	// ErrPending means a request is already waiting for the node.
	ErrPending = errors.New("backup: a request is already waiting for the node")
	// ErrNoAnswer means the node did not answer a request in time: it may not
	// be running, or may run without a backup: section.
	ErrNoAnswer = errors.New("backup: the node did not answer the request in time")
)

// RequestPath is the request file of a node whose data directory is dataDir.
func RequestPath(dataDir string) string { return filepath.Join(dataDir, requestName) }

// Ask has the running node of dataDir make a backup and returns its result.
// The data directory's database is open in the node, and so is the bus's
// store, so the command does not make the backup itself: it writes a request
// file with a random id (made 0600, never over another request), and waits
// until the node records a backup that carries the id in backup.json, up to
// AnswerWait. after is the wait (time.After); a request nobody answered is
// withdrawn. A failed backup comes back as a Result with OK false and an
// error with its reason.
func Ask(ctx context.Context, dataDir string, after AfterFunc) (Result, error) {
	id := event.RandomID()
	if err := writeRequest(dataDir, id); err != nil {
		return Result{}, err
	}
	deadline := after(AnswerWait)
	for {
		if res, err := Last(dataDir); err == nil && res.Request == id {
			if !res.OK {
				return res, errors.New(res.Reason)
			}
			return res, nil
		}
		select {
		case <-ctx.Done():
			dropRequest(dataDir, id)
			return Result{}, ctx.Err()
		case <-deadline:
			dropRequest(dataDir, id)
			return Result{}, ErrNoAnswer
		case <-after(AnswerPoll):
		}
	}
}

// writeRequest puts id in the request file, which must not exist.
func writeRequest(dataDir, id string) (err error) {
	tmp, err := os.CreateTemp(dataDir, requestName+".*.tmp")
	if err != nil {
		return fmt.Errorf("backup: request: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	_, err = tmp.WriteString(id)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("backup: request: %w", err)
	}
	// A link fails when the request file exists: one request at a time.
	if err := os.Link(tmp.Name(), RequestPath(dataDir)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrPending
		}
		return fmt.Errorf("backup: request: %w", err)
	}
	return nil
}

// readRequest is the id in the request file of dataDir; ok is false when
// there is none or it is not an id, which is removed.
func readRequest(dataDir string) (id string, ok bool) {
	f, err := os.Open(RequestPath(dataDir))
	if err != nil {
		return "", false
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, requestIDLen+1))
	if err != nil {
		return "", false
	}
	id = string(data)
	if !validRequestID(id) {
		_ = os.Remove(RequestPath(dataDir))
		return "", false
	}
	return id, true
}

// validRequestID reports whether s is an id event.RandomID makes: upper-case
// letters and digits, so it is safe in the result file and in a log line.
func validRequestID(s string) bool {
	if len(s) != requestIDLen {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && (r < 'A' || r > 'Z') }) < 0
}

// dropRequest removes the request file if it still holds id: a newer
// request is not touched.
func dropRequest(dataDir, id string) {
	if cur, ok := readRequest(dataDir); ok && cur == id {
		_ = os.Remove(RequestPath(dataDir))
	}
}
