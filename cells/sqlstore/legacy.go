package sqlstore

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/igorrius/ganglion/cells"
)

// MigrateToBus moves the cells of the old tables into to, the store on the
// bus. While a table `cells` exists, every cell in it is copied with
// cells.Migrate (one that to already has counts as copied), and only after
// the last one is in are the tables renamed to `cells_migrated` and
// `cell_changes_migrated`. A later start finds no `cells` table and copies
// nothing; a start that died halfway finds the table and finishes. It never
// creates the tables: a node that never had them, or has moved already,
// stays as it is. It returns how many cells it copied.
func MigrateToBus(ctx context.Context, db *sql.DB, to cells.Store, log *slog.Logger) (int, error) {
	var found int
	err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = 'cells'`).Scan(&found)
	if err != nil {
		return 0, fmt.Errorf("sqlstore: look for the cells table: %w", err)
	}
	if found == 0 {
		return 0, nil
	}
	// The table exists, so its migrations are not run again to make it; they
	// only bring an older table (without owner and about) up to date.
	old, err := New(ctx, db)
	if err != nil {
		return 0, err
	}
	n, err := cells.Migrate(ctx, old, to, log)
	if err != nil {
		return n, err
	}
	return n, old.retire(ctx)
}

// retire renames the tables, both or neither, to mark the migration done.
func (s *Store) retire(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("sqlstore: retire: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, q := range []string{
		`ALTER TABLE cells RENAME TO cells_migrated`,
		`ALTER TABLE cell_changes RENAME TO cell_changes_migrated`,
	} {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("sqlstore: retire: %w", err)
		}
	}
	return tx.Commit()
}
