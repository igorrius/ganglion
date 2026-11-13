package vault

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/igorrius/ganglion/sqlitedb"
)

// The check value: a constant sealed under the key the store was made
// with. A key that opens it fits the store's rows.
const (
	checkKey   = "check"
	checkName  = "check"
	checkPlain = "ganglion-secret-store"
)

// Locked reports whether the key did not fit the store at Open, or at the
// last Reset. While locked: Get returns "" and no error, Set returns
// ErrLocked, Remove and RemoveScope still delete, List still lists.
func (v *Vault) Locked() bool { return v.locked.Load() }

// fits writes the check value when the store has none, then reports
// whether the store's key opens the one that is there. Two processes that
// open a new store at once both find the same check value: the insert of
// the second changes nothing.
func (v *Vault) fits(ctx context.Context) (bool, error) {
	nonce, ciphertext, err := seal(v.aead, "", checkName, []byte(checkPlain))
	if err != nil {
		return false, err
	}
	if _, err := v.db.ExecContext(ctx,
		`INSERT INTO secret_meta (key, nonce, ciphertext) VALUES (?, ?, ?) ON CONFLICT (key) DO NOTHING`,
		checkKey, nonce, ciphertext); err != nil {
		return false, err
	}
	if err := v.db.QueryRowContext(ctx,
		`SELECT nonce, ciphertext FROM secret_meta WHERE key = ?`, checkKey).Scan(&nonce, &ciphertext); err != nil {
		return false, err
	}
	plain, err := open(v.aead, "", checkName, nonce, ciphertext)
	return err == nil && string(plain) == checkPlain, nil
}

// Reset deletes every secret, writes the check value for the key the
// store was opened with, and unlocks it. It returns how many it deleted.
func (v *Vault) Reset(ctx context.Context) (int, error) {
	n, err := v.reset(ctx)
	v.in.count(ctx, opReset, done(err))
	if err != nil {
		return 0, fmt.Errorf("vault: reset: %w", err)
	}
	v.locked.Store(false)
	return n, nil
}

func (v *Vault) reset(ctx context.Context) (int, error) {
	nonce, ciphertext, err := seal(v.aead, "", checkName, []byte(checkPlain))
	if err != nil {
		return 0, err
	}
	var dropped int64
	err = sqlitedb.Immediate(ctx, v.db, func(c *sql.Conn) error {
		res, err := c.ExecContext(ctx, `DELETE FROM secrets`)
		if err != nil {
			return err
		}
		if dropped, err = res.RowsAffected(); err != nil {
			return err
		}
		_, err = c.ExecContext(ctx,
			`INSERT OR REPLACE INTO secret_meta (key, nonce, ciphertext) VALUES (?, ?, ?)`, checkKey, nonce, ciphertext)
		return err
	})
	return int(dropped), err
}
