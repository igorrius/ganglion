package cellskv

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/nats-io/nats.go/jetstream"
)

// entry is the value of one KV key: the cell as it stands and the change
// that wrote it, or nil for a write that made no change (SetOwner, Disown).
type entry struct {
	Cell   cellJSON    `json:"cell"`
	Change *changeJSON `json:"change"`
}

// cellJSON is the stored shape of a cells.Cell.
type cellJSON struct {
	ID       string          `json:"id"`
	Kind     cells.Kind      `json:"kind"`
	Backend  string          `json:"backend,omitempty"`
	Desired  json.RawMessage `json:"desired,omitempty"`
	Reported json.RawMessage `json:"reported,omitempty"`
	Cause    cells.Cause     `json:"cause,omitempty"`
	Version  int64           `json:"version"`
	Updated  time.Time       `json:"updated"`
	Expires  time.Time       `json:"expires,omitzero"`
	Owner    string          `json:"owner,omitempty"`
	About    string          `json:"about,omitempty"`
}

// changeJSON is the stored shape of a cells.Change. The cell's id is the
// entry's, and the sequence is the entry's revision, so neither is kept.
type changeJSON struct {
	Version int64           `json:"version"`
	Field   string          `json:"field"`
	Value   json.RawMessage `json:"value,omitempty"`
	Cause   cells.Cause     `json:"cause,omitempty"`
	Source  string          `json:"source,omitempty"`
	At      time.Time       `json:"at"`
}

func toCellJSON(c cells.Cell) cellJSON {
	return cellJSON{ID: c.ID, Kind: c.Kind, Backend: c.Backend, Desired: c.Desired, Reported: c.Reported,
		Cause: c.ReportedCause, Version: c.Version, Updated: c.Updated, Expires: c.Expires, Owner: c.Owner, About: c.About}
}

func (c cellJSON) cell() cells.Cell {
	return cells.Cell{ID: c.ID, Kind: c.Kind, Backend: c.Backend, Desired: c.Desired, Reported: c.Reported,
		ReportedCause: c.Cause, Version: c.Version, Updated: c.Updated, Expires: c.Expires, Owner: c.Owner, About: c.About}
}

func toChangeJSON(ch cells.Change) *changeJSON {
	return &changeJSON{Version: ch.Version, Field: ch.Field, Value: ch.Value, Cause: ch.Cause, Source: ch.Source, At: ch.At}
}

// change returns the entry's change as seen at revision seq, or false when
// the write made none.
func (e entry) change(seq uint64) (cells.Change, bool) {
	c := e.Change
	if c == nil {
		return cells.Change{}, false
	}
	return cells.Change{Seq: int64(seq), CellID: e.Cell.ID, Version: c.Version, Field: c.Field, Value: c.Value,
		Cause: c.Cause, Source: c.Source, At: c.At}, true
}

func encode(c cells.Cell, ch *cells.Change) ([]byte, error) {
	e := entry{Cell: toCellJSON(c)}
	if ch != nil {
		e.Change = toChangeJSON(*ch)
	}
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("cellskv: encode %s: %w", c.ID, err)
	}
	return b, nil
}

func decode(data []byte) (entry, error) {
	var e entry
	if err := json.Unmarshal(data, &e); err != nil {
		return entry{}, fmt.Errorf("cellskv: decode: %w", err)
	}
	return e, nil
}

// isLive reports whether a KV entry holds a value (not a delete marker).
func isLive(e jetstream.KeyValueEntry) bool {
	return e.Operation() == jetstream.KeyValuePut
}

// key maps a cell id to a KV key. NATS allows [-/_=.a-zA-Z0-9] in a key, and
// no dot at the start or the end or two dots in a row. An id keeps every
// allowed character as it is; any other byte, the "=" itself, and a dot NATS
// would refuse become "=XX" (the byte in upper-case hex), so two ids never
// meet in one key and the id can be read back from its key.
func key(id string) string {
	var b strings.Builder
	for i := 0; i < len(id); i++ {
		ch := id[i]
		if plain(ch) && (ch != '.' || !badDot(id, i)) {
			b.WriteByte(ch)
			continue
		}
		fmt.Fprintf(&b, "=%02X", ch)
	}
	return b.String()
}

// plain reports whether ch is kept as it is: a NATS key character other
// than "=".
func plain(ch byte) bool {
	switch {
	case ch >= 'a' && ch <= 'z', ch >= 'A' && ch <= 'Z', ch >= '0' && ch <= '9':
		return true
	}
	return ch == '-' || ch == '/' || ch == '_' || ch == '.'
}

// badDot reports whether the dot at id[i] opens, ends or doubles a run.
func badDot(id string, i int) bool {
	return i == 0 || i == len(id)-1 || id[i-1] == '.'
}
