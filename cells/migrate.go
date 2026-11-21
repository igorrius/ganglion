package cells

import (
	"context"
	"fmt"
	"log/slog"
)

// MigrationSource is the Source of the change a migrated cell starts with.
const MigrationSource = "migration"

// Migrate copies every cell of from into to when to holds none and from
// holds some: each with its current value, version, owner and about, and one
// change from MigrationSource. A store that already has cells is left alone,
// so a second start copies nothing. It returns how many cells it copied and
// logs the count.
//
// The history of the old store is not carried over. Cells are written one by
// one, so a start that dies halfway leaves a part of them in to, and the next
// start takes that part for the whole.
func Migrate(ctx context.Context, from, to Store, log *slog.Logger) (int, error) {
	have, err := to.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("cells: migrate: %w", err)
	}
	if len(have) > 0 {
		return 0, nil
	}
	old, err := from.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("cells: migrate: %w", err)
	}
	for _, c := range old {
		if _, err := to.Create(ctx, c, MigrationSource); err != nil {
			return 0, fmt.Errorf("cells: migrate %s: %w", c.ID, err)
		}
	}
	if len(old) > 0 {
		log.Info("cells: migrated to the bus", "cells", len(old))
	}
	return len(old), nil
}
