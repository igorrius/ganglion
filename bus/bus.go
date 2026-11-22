// Package bus holds the names the node's message bus uses: the KV bucket, the
// streams, the subjects and the message headers (R-BUS-2), the mode the
// server runs in and the errors a bus gives. It imports no NATS package;
// bus/natsbus is the one place that does (R-BUS-1), and the consumers of
// the bus declare the small interfaces they need themselves.
package bus

import (
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// Mode says where the bus's server runs (R-BUS-1).
type Mode string

const (
	// ModeEmbedded runs nats-server inside the node's process.
	ModeEmbedded Mode = "embedded"
	// ModeExternal connects to a server, or a cluster, the operator runs.
	ModeExternal Mode = "external"
)

// Names of the bucket and the streams (R-BUS-2).
const (
	// BucketCells is the KV bucket that holds the state cells.
	BucketCells = "cells"
	// StreamNotify keeps the modules' notices.
	StreamNotify = "NOTIFY"
	// StreamEvents keeps the modules' events.
	StreamEvents = "EVENTS"
)

// The wildcard subjects the streams cover.
const (
	// SubjectsNotify is what the NOTIFY stream keeps.
	SubjectsNotify = "g.notify.>"
	// SubjectsEvents is what the EVENTS stream keeps.
	SubjectsEvents = "g.ev.>"
)

// subjectRoot is the first token of every subject the node uses.
const subjectRoot = "g"

// The headers every message carries (R-BUS-2). The trace context travels in
// the W3C headers the telemetry package sets.
const (
	// HeaderMsgID is the idempotency key; JetStream drops a duplicate within
	// the stream's window.
	HeaderMsgID = "Nats-Msg-Id"
	// HeaderFrom names the producing node and module.
	HeaderFrom = "Ganglion-From"
	// HeaderDepth is the chain depth of the message.
	HeaderDepth = "Ganglion-Depth"
	// HeaderRun names the run that produced the message.
	HeaderRun = "Ganglion-Run"
	// HeaderPort names the calling port of a request, in the caller's module.
	HeaderPort = "Ganglion-Port"
)

// Sentinel errors of a bus.
var (
	// ErrNotConnected is returned when the bus has no connection to its
	// server.
	ErrNotConnected = errors.New("bus: not connected")
	// ErrBadToken is returned for a name that cannot be a subject token.
	ErrBadToken = errors.New("bus: bad subject token")
	// ErrBadURL is returned for a server address that is not nats:// or
	// tls://.
	ErrBadURL = errors.New("bus: bad server url")
)

// CheckToken reports whether s can be one token of a subject: not empty and
// without a dot, a wildcard, a space or a control character.
func CheckToken(s string) error {
	if s == "" || strings.ContainsAny(s, ".*> \t\r\n") {
		return fmt.Errorf("%w: %q", ErrBadToken, s)
	}
	return nil
}

// subject joins checked tokens under the subject root.
func subject(kind string, tokens ...string) (string, error) {
	for _, t := range tokens {
		if err := CheckToken(t); err != nil {
			return "", err
		}
	}
	return strings.Join(append([]string{subjectRoot, kind}, tokens...), "."), nil
}

// Notify is the subject of a module's notices: g.notify.<module>.
func Notify(module string) (string, error) { return subject("notify", module) }

// Event is the subject of a module's events: g.ev.<node>.<module>.<port>.
func Event(node, module, port string) (string, error) { return subject("ev", node, module, port) }

// Call is the subject of a call to a module's port: g.rpc.<node>.<module>.<port>.
func Call(node, module, port string) (string, error) { return subject("rpc", node, module, port) }

// Telemetry is the subject of a node's metrics: g.tel.<node>.
func Telemetry(node string) (string, error) { return subject("tel", node) }

// CheckURL reports whether s is the address of a NATS server: nats:// or
// tls:// with a host, and nothing in front of it that holds a secret. An
// error never repeats the value, which may hold a password.
func CheckURL(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "nats" && u.Scheme != "tls") || u.Hostname() == "" || u.User != nil {
		return fmt.Errorf("%w: must be nats:// or tls:// with a host and no user or password", ErrBadURL)
	}
	return nil
}

// StorePath is the embedded server's store folder for a node whose data
// lives in dataDir: bus beside the database.
func StorePath(dataDir string) string { return filepath.Join(dataDir, "bus") }
