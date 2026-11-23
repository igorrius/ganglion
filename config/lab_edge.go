//go:build lab

package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/igorrius/ganglion/bus"
	"github.com/igorrius/ganglion/bus/natsbus"
	"github.com/igorrius/ganglion/modhost"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/settings"
	"github.com/samber/do/v2"
)

// labEdgeTelemetry is how often a lab edge speaks: the core resyncs an edge
// it heard after a silence of three of these, so a scenario sees the events
// of a restored link within its patience.
const labEdgeTelemetry = 300 * time.Millisecond

// LabEdgeOptions are what chatlab gives an edge that runs beside the lab
// node, in the same process.
type LabEdgeOptions struct {
	Name    string         // the name the edge joined as
	Creds   []byte         // the credentials the join gave
	URL     string         // the leaf's address, ws://host:port of a link to the node's entry
	Dir     string         // the edge's own folder: its database, modules and bus
	Secrets secrets.Source // the modules' secrets, as the node's
	Admin   string         // the owner's principal, for the module store's records
}

// LabEdge is an edge role built as `ganglion run --roles edge` builds it,
// with what chatlab runs and reads.
type LabEdge struct {
	Components []node.Component
	// Linked reports whether its leaf link to the node is up.
	Linked func(ctx context.Context) bool
	// Held is how many events its own stream holds.
	Held func(ctx context.Context) uint64
	// Emit makes n events as a module of the edge would: each is kept in the
	// edge's stream and travels to the node when the link is up.
	Emit func(ctx context.Context, n int) error
	// Running is the version of module that runs on the edge; "" when none.
	Running  func(ctx context.Context, module string) (string, error)
	Shutdown func(context.Context)
}

// NewLabEdge builds the edge role of o. It does not run it: node.Run does,
// with Components, the way the node's own are run.
func NewLabEdge(ctx context.Context, o LabEdgeOptions, logger *slog.Logger) (*LabEdge, error) {
	creds := filepath.Join(o.Dir, edgeCredsFile)
	if err := os.MkdirAll(o.Dir, 0o755); err != nil {
		return nil, fmt.Errorf("lab: edge folder: %w", err)
	}
	if err := os.WriteFile(creds, o.Creds, 0o600); err != nil {
		return nil, fmt.Errorf("lab: edge credentials: %w", err)
	}
	var file settings.File
	file.Edge = settings.Edge{Name: o.Name, URL: o.URL, Credentials: creds, TelemetryEvery: settings.Duration(labEdgeTelemetry)}
	b := Bootstrap{DB: filepath.Join(o.Dir, "node.db"), Admin: o.Admin, Secrets: o.Secrets, MasterKey: filepath.Join(o.Dir, vault.KeyFile),
		ModulesDir: filepath.Join(o.Dir, "modules"), MetricsAddr: "off", Settings: file, EdgeRole: true, ServesPages: true}
	i := New(ctx, b, logger)
	applyEdge(i)
	components, err := do.Invoke[[]node.Component](i)
	if err != nil {
		return nil, err
	}
	nb, host := do.MustInvoke[*natsbus.Bus](i), do.MustInvoke[*modhost.Host](i)
	var seq atomic.Int64
	return &LabEdge{
		Components: components,
		Linked:     func(ctx context.Context) bool { return nb.EdgeHealth(ctx).Linked },
		Held:       func(ctx context.Context) uint64 { return nb.EdgeHealth(ctx).Events },
		Emit: func(ctx context.Context, n int) error {
			subject, err := bus.Event(o.Name, "lab", "reading")
			if err != nil {
				return err
			}
			for range n {
				k := seq.Add(1)
				if err := nb.Publish(ctx, subject, "lab-"+strconv.FormatInt(k, 10), nil, []byte(strconv.FormatInt(k, 10))); err != nil {
					return err
				}
			}
			return nil
		},
		Running: func(ctx context.Context, module string) (string, error) {
			local, err := host.Local(ctx)
			if err != nil {
				return "", err
			}
			for _, m := range local {
				if m.Name == module && m.State == modstore.RunRunning {
					return m.Version, nil
				}
			}
			return "", nil
		},
		Shutdown: func(ctx context.Context) { _ = i.ShutdownWithContext(ctx) },
	}, nil
}
