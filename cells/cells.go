// Package cells implements state cells (ADR-0014): named pieces of state
// with a desired value (what a flow or the agent wants) and a reported value
// (what is actually true), a version and a history.
//
// Kinds: a switch has desired and reported "on"/"off"; a sensor has only a
// reported value; a request is a human confirmation that is pending until
// approved, denied or expired. Backends reconcile reported with desired;
// observers follow changes through Service.Watch.
package cells

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Kind is the kind of a cell.
type Kind string

// Cell kinds.
const (
	KindSwitch  Kind = "switch"
	KindSensor  Kind = "sensor"
	KindRequest Kind = "request"
)

// Cause tells why the reported value changed.
type Cause string

// Report causes.
const (
	// CauseCommanded: the backend reports the result of our desired value.
	CauseCommanded Cause = "commanded"
	// CauseExternal: the state changed by itself or by hand.
	CauseExternal Cause = "external"
)

// Change fields.
const (
	FieldCreated  = "created"
	FieldDesired  = "desired"
	FieldReported = "reported"
	FieldError    = "error"
)

// Cell is the current state of one cell.
type Cell struct {
	ID            string
	Kind          Kind
	Backend       string          // backend name; "" for none
	Desired       json.RawMessage // nil when unset
	Reported      json.RawMessage // nil when unset
	ReportedCause Cause
	Version       int64 // 1 on create, +1 on every change
	Updated       time.Time
	Expires       time.Time // requests only; zero otherwise
}

// Change is one entry of a cell's history and of the global change stream.
type Change struct {
	Seq     int64 // global, assigned by the store, strictly increasing
	CellID  string
	Version int64 // the cell's version after the change
	Field   string
	Value   json.RawMessage
	Cause   Cause  // set for reported changes
	Source  string // who made the change: "flow:load-shedding", "agent", "backend:virtual", "owner"
	At      time.Time
}

// Errors returned by stores and the service.
var (
	ErrNotFound = errors.New("cells: not found")
	ErrExists   = errors.New("cells: already exists")
	ErrConflict = errors.New("cells: version conflict")
)

// Store keeps cells and their changes. Implementations: MemStore and
// cells/sqlstore; both pass cells/storetest.
type Store interface {
	// Create stores a new cell and records a "created" change.
	Create(ctx context.Context, c Cell, src string) (Change, error)
	// Get returns a cell or ErrNotFound.
	Get(ctx context.Context, id string) (Cell, error)
	// List returns all cells sorted by ID.
	List(ctx context.Context) ([]Cell, error)
	// Update stores c with Version prevVersion+1 and appends ch, atomically.
	// It returns ErrConflict if the stored version is not prevVersion.
	Update(ctx context.Context, c Cell, prevVersion int64, ch Change) (Change, error)
	// ChangesSince returns up to limit changes with Seq > seq, ascending.
	ChangesSince(ctx context.Context, seq int64, limit int) ([]Change, error)
	// History returns up to limit changes of one cell, newest first.
	History(ctx context.Context, id string, limit int) ([]Change, error)
	// PendingRequestsBefore returns pending request cells expiring at or
	// before t.
	PendingRequestsBefore(ctx context.Context, t time.Time) ([]Cell, error)
}

// requestStatus reads the status of a request cell's reported value.
func requestStatus(c Cell) string {
	var d struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(c.Reported, &d)
	return d.Status
}

// IsPendingRequest reports whether c is a request still waiting for an answer.
func IsPendingRequest(c Cell) bool {
	return c.Kind == KindRequest && requestStatus(c) == StatusPending
}

// Request statuses.
const (
	StatusPending  = "pending"
	StatusApproved = "approved"
	StatusDenied   = "denied"
	StatusExpired  = "expired"
)
