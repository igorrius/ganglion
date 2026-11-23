package edge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/runner"
)

// This file is the core's side of module delivery (R-EDGE-6, ADR-0117) and
// the values both sides write: the core puts a build's files into the
// modules Object Store and Desired into desired.<edge>.<module>; the edge
// writes Actual into actual.<edge>.<module>.

// DeliveryVersion is the format of the delivery values this node writes.
const DeliveryVersion = 1

// Desired is what an edge should run of a module. The edge and the module
// are the key's; the module is repeated so a value moved to another key
// does not pass.
type Desired struct {
	V       int    `json:"v"`
	Module  string `json:"module"`  // the short name
	Version string `json:"version"` // the manifest's version, for the owner's words
	// SHA256 is the build's modstore.Digest, over the .wasm and the
	// manifest: what the edge checks the files by before it runs anything.
	SHA256 string         `json:"sha256"`
	Config map[string]any `json:"config,omitempty"`
}

// Actual is what an edge runs of a module, written after the told version
// passed its init and health, or was refused.
type Actual struct {
	V       int       `json:"v"`
	Module  string    `json:"module"`
	Version string    `json:"version,omitempty"` // the version that runs; "" when none
	SHA256  string    `json:"sha256,omitempty"`  // its digest
	Desired string    `json:"desired"`           // the digest the edge was told
	Result  string    `json:"result"`            // modstore.EdgeRunning, EdgeRefused or EdgeRemoved
	Reason  string    `json:"reason,omitempty"`  // why the told version was refused, in the node's words
	At      time.Time `json:"at"`
}

// MaxDesired is the largest desired value an edge reads.
const MaxDesired = 64 << 10

var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// errDesired is a desired value an edge does not take.
var errDesired = errors.New("edge: not a desired value")

// decodeDesired reads a desired value of module, refusing one too large, of
// another format or module, with a name that cannot be a module's, a
// digest that is not one, or a version that is not a number.
func decodeDesired(module string, data []byte) (Desired, error) {
	var d Desired
	if len(data) > MaxDesired || json.Unmarshal(data, &d) != nil || d.V != DeliveryVersion || d.Module != module ||
		modstore.CheckShortName(module) != "" || !digestRE.MatchString(d.SHA256) {
		return Desired{}, errDesired
	}
	if _, err := strconv.Atoi(d.Version); err != nil {
		return Desired{}, errDesired
	}
	return d, nil
}

// DeliveryBus is the core's side of the bus for delivery
// (natsbus.Bus).
type DeliveryBus interface {
	PutModule(ctx context.Context, module, digest string, wasm, manifest []byte) error
	PutDesired(ctx context.Context, edge, module string, value []byte) error
	DeleteDesired(ctx context.Context, edge, module string) error
	DesiredEdges(ctx context.Context, module string) ([]string, error)
	PruneModule(ctx context.Context, module string, keep ...string) error
	Actual(ctx context.Context, edge, module string) ([]byte, bool, error)
}

// Deliverer is the core's delivery of modules to edges: the module host's
// modhost.Delivery.
type Deliverer struct {
	Bus DeliveryBus
}

// Deliver writes a's files, then what edge should run: files first, so an
// edge never reads a digest whose files are not there yet. The other
// builds' files of the module go, except the one edge runs now, which a
// rollback may want back; the core keeps the files a rollback needs and
// writes them again.
func (d Deliverer) Deliver(ctx context.Context, edge string, a modstore.Active, config map[string]any) error {
	short := modstore.ShortName(a.Module)
	m, err := runner.ParseManifest(a.Manifest)
	if err != nil {
		return fmt.Errorf("edge: %s: the manifest does not load: %w", a.Module, err)
	}
	if err := d.Bus.PutModule(ctx, short, a.SHA256, a.Wasm, a.Manifest); err != nil {
		return err
	}
	value, err := json.Marshal(Desired{V: DeliveryVersion, Module: short, Version: strconv.Itoa(m.Version), SHA256: a.SHA256, Config: config})
	if err != nil {
		return err
	}
	if err := d.Bus.PutDesired(ctx, edge, short, value); err != nil {
		return err
	}
	keep := []string{a.SHA256}
	if run, ok, err := d.Actual(ctx, edge, short); err == nil && ok && run.SHA256 != "" {
		keep = append(keep, run.SHA256)
	}
	return d.Bus.PruneModule(ctx, short, keep...)
}

// Withdraw drops what every edge but keep should run of module; with keep
// "" its files go as well.
func (d Deliverer) Withdraw(ctx context.Context, module, keep string) error {
	edges, err := d.Bus.DesiredEdges(ctx, module)
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range edges {
		if e != keep {
			errs = append(errs, d.Bus.DeleteDesired(ctx, e, module))
		}
	}
	if keep == "" {
		errs = append(errs, d.Bus.PruneModule(ctx, module))
	}
	return errors.Join(errs...)
}

// Actual reads what edge says it runs of module. A value that is not one,
// or names another module, is no answer.
func (d Deliverer) Actual(ctx context.Context, edge, module string) (modstore.EdgeRun, bool, error) {
	data, ok, err := d.Bus.Actual(ctx, edge, module)
	if err != nil || !ok {
		return modstore.EdgeRun{}, false, err
	}
	var a Actual
	if len(data) > MaxDesired || json.Unmarshal(data, &a) != nil || a.V != DeliveryVersion || a.Module != module ||
		!slices.Contains([]string{modstore.EdgeRunning, modstore.EdgeRefused, modstore.EdgeRemoved}, a.Result) {
		return modstore.EdgeRun{}, false, nil
	}
	// The edge's words reach the owner: a version that is not a number and
	// a digest that is not one are dropped, a reason is cut short.
	if _, err := strconv.Atoi(a.Version); err != nil {
		a.Version = ""
	}
	for _, d := range []*string{&a.SHA256, &a.Desired} {
		if !digestRE.MatchString(*d) {
			*d = ""
		}
	}
	return modstore.EdgeRun{Node: edge, Version: a.Version, SHA256: a.SHA256, Desired: a.Desired, Result: a.Result,
		Reason: text.Truncate(a.Reason, maxReason)}, true, nil
}

// maxReason bounds the reason an edge gives that the owner reads.
const maxReason = 200
