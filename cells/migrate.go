package cells

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
)

// MigrationSource is the Source of the change a migrated cell starts with.
const MigrationSource = "migration"

// Migrate copies every cell of from into to: each with its current value,
// version, owner and about, and one change from MigrationSource. A cell that
// to already has counts as copied and is left as it is, so a run that follows
// a crash finishes the job and never overwrites a value written since. It
// returns how many cells it copied and logs the count. The old store's
// history is not carried over.
//
// Migrate does not say when the migration is over: the caller marks the old
// store done after Migrate returns without an error, and calls it again
// until then.
func Migrate(ctx context.Context, from, to Store, log *slog.Logger) (int, error) {
	old, err := from.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("cells: migrate: %w", err)
	}
	n := 0
	for _, c := range old {
		_, err := to.Create(ctx, c, MigrationSource)
		if errors.Is(err, ErrExists) {
			continue
		}
		if err != nil {
			return n, fmt.Errorf("cells: migrate %s: %w", c.ID, err)
		}
		n++
	}
	if len(old) > 0 {
		log.Info("cells: migrated to the bus", "cells", n, "already there", len(old)-n)
	}
	return n, nil
}
