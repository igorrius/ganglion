package agent

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Prune deletes the archived sessions that ended before cut, except one the memory review still has
// to read: a review_due row names it (R-MEM-1, ADR-0104). It returns how many it deleted.
func (a *Archive) Prune(ctx context.Context, cut time.Time) (int, error) {
	res, err := a.db.ExecContext(ctx, `DELETE FROM sessions WHERE ended_ms < ? AND NOT EXISTS
		(SELECT 1 FROM review_due r WHERE r.conv = sessions.conv AND r.session = sessions.session)`, ms(cut))
	if err != nil {
		return 0, fmt.Errorf("agent: prune sessions: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("agent: prune sessions: %w", err)
	}
	return int(n), nil
}

// ArchivePruner drops archived sessions older than Keep, at start and then every Every; a Keep of 0
// keeps them all (memory.archive_days: 0). It is a node component, with no model.
type ArchivePruner struct {
	Archive *Archive
	Keep    time.Duration
	Every   time.Duration
	Now     func() time.Time
	Log     *slog.Logger
}

// Name implements node.Component.
func (ArchivePruner) Name() string { return "session-archive" }

// Run prunes until ctx ends, then returns nil. A failed pass is logged and tried again next time.
func (p ArchivePruner) Run(ctx context.Context) error {
	for {
		p.pass(ctx)
		timer := time.NewTimer(p.Every)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// pass is one pruning: it logs and counts what it dropped, or that it failed.
func (p ArchivePruner) pass(ctx context.Context) {
	if p.Keep <= 0 {
		return
	}
	pruned := prunedCounter()
	n, err := p.Archive.Prune(ctx, p.Now().Add(-p.Keep))
	if err != nil {
		if ctx.Err() == nil {
			p.Log.Error("agent: archived sessions not pruned", "err", err)
			pruned.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "failed")))
		}
		return
	}
	if n > 0 {
		p.Log.Info("agent: pruned archived sessions", "sessions", n, "older_than", p.Keep.String())
	}
	pruned.Add(ctx, int64(n), metric.WithAttributes(attribute.String("outcome", "ok")))
}
