package sqlqueue

import (
	"context"
	"log/slog"
	"time"
)

// Pruner deletes finished queue rows older than Retention, at start and
// then every Every. It is a node component.
type Pruner struct {
	Queue *Queue
	Every time.Duration
	Log   *slog.Logger
}

// Name implements node.Component.
func (Pruner) Name() string { return "prune" }

// Run prunes until ctx ends, then returns nil. A failed prune is logged
// and tried again next time.
func (p Pruner) Run(ctx context.Context) error {
	for {
		if n, err := p.Queue.Prune(ctx, Retention); err != nil {
			if ctx.Err() != nil {
				return nil
			}
			p.Log.Error("node: prune failed", "err", err)
		} else if n > 0 {
			p.Log.Info("node: pruned finished queue rows", "rows", n)
		}
		timer := time.NewTimer(p.Every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}
