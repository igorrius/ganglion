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
	// SubjectsTelemetry is every node's telemetry, one token per node.
	SubjectsTelemetry = "g.tel.*"
	// TelemetryPrefix is the part of a telemetry subject before the node.
	TelemetryPrefix = "g.tel."
)

// CoreNode is the core's token in subjects and its JetStream domain's
// place: no edge may take this name.
const CoreNode = "core"

// CheckEdgeName reports whether s can name an edge: one subject token, and
// not the core's own name.
func CheckEdgeName(s string) error {
	if err := CheckToken(s); err != nil {
		return err
	}
	if s == CoreNode {
		return fmt.Errorf("%w: %q is the core's name", ErrBadToken, s)
	}
	return nil
}

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

// Module delivery to edges (R-EDGE-6, ADR-0117): the core keeps a
// module's files in an Object Store and what each edge should run in a KV
// bucket; the edge writes back what it runs. Both live in the core's
// JetStream domain, CoreNode, which an edge reaches across its link.
const (
	// BucketDelivery is the KV bucket of desired.<edge>.<module> and
	// actual.<edge>.<module>.
	BucketDelivery = "delivery"
	// ObjectModules is the Object Store of the modules' files.
	ObjectModules = "modules"
)

// The first tokens of the delivery keys.
const (
	desiredRoot = "desired"
	actualRoot  = "actual"
)

// key joins checked tokens with dots: a KV key of one token per part.
func key(root string, tokens ...string) (string, error) {
	for _, t := range tokens {
		if err := CheckToken(t); err != nil {
			return "", err
		}
	}
	return strings.Join(append([]string{root}, tokens...), "."), nil
}

// Desired is the key of what edge should run of module:
// desired.<edge>.<module>, the module by its short name.
func Desired(edge, module string) (string, error) {
	if err := CheckEdgeName(edge); err != nil {
		return "", err
	}
	return key(desiredRoot, edge, module)
}

// Actual is the key of what edge runs of module: actual.<edge>.<module>.
func Actual(edge, module string) (string, error) {
	if err := CheckEdgeName(edge); err != nil {
		return "", err
	}
	return key(actualRoot, edge, module)
}

// DesiredOf is the pattern of every desired key of edge:
// desired.<edge>.*.
func DesiredOf(edge string) (string, error) {
	if err := CheckEdgeName(edge); err != nil {
		return "", err
	}
	return desiredRoot + "." + edge + ".*", nil
}

// DesiredOfModule is the pattern of module's desired key on every edge:
// desired.*.<module>.
func DesiredOfModule(module string) (string, error) {
	if err := CheckToken(module); err != nil {
		return "", err
	}
	return desiredRoot + ".*." + module, nil
}

// ParseDesired splits a desired key into its edge and module; false when
// key is not one.
func ParseDesired(k string) (edge, module string, ok bool) {
	parts := strings.Split(k, ".")
	if len(parts) != 3 || parts[0] != desiredRoot || CheckEdgeName(parts[1]) != nil || CheckToken(parts[2]) != nil {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// The parts of a module's build in the Object Store.
const (
	PartWasm     = "wasm"
	PartManifest = "yaml"
)

// ModuleObject is the name of one part of a build in the Object Store:
// <module>.<digest>.<part>. A build's objects are written once and never
// changed; the digest in the desired key is what an edge checks them by.
func ModuleObject(module, digest, part string) (string, error) {
	if err := CheckToken(module); err != nil {
		return "", err
	}
	return key(module, digest, part)
}

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
