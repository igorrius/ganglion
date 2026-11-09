package cells

import (
	"context"
	"log/slog"
	"time"
)

// ReconcileEvery is how often the node reconciles its cells (design
// section 8.5).
const ReconcileEvery = 5 * time.Minute

// AfterFunc waits like time.After.
type AfterFunc func(d time.Duration) <-chan time.Time

// Reconciler is a node component: it reconciles the service's cells at
// start and then every period, so a switch whose apply a restart cut short
// is acted on (Service.Reconcile).
type Reconciler struct {
	Service *Service
	Every   time.Duration
	Log     *slog.Logger
	After   AfterFunc // time.After in the node
}

// Name implements node.Component.
func (Reconciler) Name() string { return "reconcile-cells" }

// Run reconciles until ctx ends, then returns nil. A failed pass is logged
// and the next one tries again.
func (r Reconciler) Run(ctx context.Context) error {
	for {
		if n, err := r.Service.Reconcile(ctx); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			r.Log.Error("cells: reconcile failed", "applied", n, "err", err)
		} else if n > 0 {
			r.Log.Info("cells: reconciled", "applied", n)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-r.After(r.Every):
		}
	}
}
