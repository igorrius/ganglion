// Package restart tells the person who asked for a restart from the chat
// that the node is back (R-CFG-6, #238). Before the restart the agent
// records who asked, where and when in a small file beside the database;
// at start Back takes the record, removes it and says the node is back,
// how long it took and which changes from the chat took effect with it.
// /update keeps its own messages and records nothing here.
package restart

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// FileName is the record's file, beside the database.
const FileName = "restart.json"

// Request is a restart someone asked for from the chat.
type Request struct {
	// Principal asked; the message is in their language.
	Principal string `json:"principal"`
	// Transport and Conv are where they asked, and where the node answers.
	Transport string `json:"transport"`
	Conv      string `json:"conv"`
	// At is when they asked.
	At time.Time `json:"at"`
	// Since is when the process that restarted had started: changes from
	// the chat after it took effect with this restart.
	Since time.Time `json:"since"`
}

// Asker records a restart asked from the chat (Note).
type Asker interface {
	Ask(principal, transport, conv string) error
}

// NoAsker records nothing: a node whose database is in memory.
type NoAsker struct{}

// Ask implements Asker.
func (NoAsker) Ask(string, string, string) error { return nil }

// Note is the record in a file.
type Note struct {
	path  string
	since time.Time
	now   func() time.Time
}

// NewNote is the record at path; since is when this process started.
func NewNote(path string, since time.Time, now func() time.Time) *Note {
	return &Note{path: path, since: since, now: now}
}

// Ask implements Asker: it writes the record through a temporary file
// and a rename, so a crash leaves the old record or the new one.
func (n *Note) Ask(principal, transport, conv string) error {
	b, err := json.Marshal(Request{Principal: principal, Transport: transport, Conv: conv, At: n.now().UTC(), Since: n.since.UTC()})
	if err != nil {
		return fmt.Errorf("restart: record: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(n.path), ".restart-*.tmp")
	if err != nil {
		return fmt.Errorf("restart: record: %w", err)
	}
	name := tmp.Name()
	_, werr := tmp.Write(b)
	cerr := tmp.Close()
	if err := errors.Join(werr, cerr); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("restart: record: %w", err)
	}
	if err := os.Rename(name, n.path); err != nil {
		_ = os.Remove(name)
		return fmt.Errorf("restart: record: %w", err)
	}
	return nil
}

// Take returns the record and removes it; false when there is none. A
// record that cannot be read is removed too, so it is never told twice.
func (n *Note) Take() (Request, bool, error) {
	b, err := os.ReadFile(n.path)
	if errors.Is(err, fs.ErrNotExist) {
		return Request{}, false, nil
	}
	if err != nil {
		return Request{}, false, fmt.Errorf("restart: read the record: %w", err)
	}
	if err := os.Remove(n.path); err != nil {
		return Request{}, false, fmt.Errorf("restart: remove the record: %w", err)
	}
	var r Request
	if err := json.Unmarshal(b, &r); err != nil || r.Conv == "" || r.Transport == "" {
		return Request{}, false, fmt.Errorf("restart: a record that is not one: %w", errors.Join(err, errNoPlace))
	}
	return r, true, nil
}

// errNoPlace is a record that names no conversation.
var errNoPlace = errors.New("no conversation")
