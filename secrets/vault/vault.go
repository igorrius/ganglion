package vault

import (
	"context"
	"crypto/cipher"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sync/atomic"
	"time"

	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/sqlitedb"
)

//go:embed migrations/*.sql
var migrations embed.FS

// component is the store's name for the migrator (ADR-0016).
const component = "secrets"

// MaxValue is the longest value, in bytes.
const MaxValue = 4096

// Errors of the store. None of them holds a value.
var (
	ErrName    = errors.New("vault: not a secret's name")
	ErrEmpty   = errors.New("vault: an empty value")
	ErrTooLong = errors.New("vault: a value over 4096 bytes")
	ErrLocked  = errors.New("vault: the store is locked: its key does not fit")
	// ErrReserved is a name kept for the node's own keys (secrets.Reserved):
	// the store's ordinary methods refuse it, and only Internal reaches it.
	ErrReserved = errors.New("vault: the name is reserved for the node's own keys")

	errNonce = errors.New("vault: a nonce of the wrong length")
)

// Entry is a stored secret without its value.
type Entry struct {
	Scope secrets.Scope
	Name  string
	SetAt time.Time
	By    string // "cli", or the principal who set it
}

// Vault is the store. Safe for concurrent use and beside another process
// that writes the same table.
type Vault struct {
	db   *sql.DB
	aead cipher.AEAD
	now  func() time.Time
	in   instruments

	locked atomic.Bool // the key does not fit the stored rows
	opened time.Time   // when Open ran: since when a locked store is locked
}

// Open migrates the "secrets" component and returns the store on key. A
// key that does not fit the rows already stored is not an error: the
// store opens locked (Locked), and nothing in it is changed.
func Open(ctx context.Context, db *sql.DB, key Key, now func() time.Time) (*Vault, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, component, sub); err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	v := &Vault{db: db, aead: aead, now: now, in: newInstruments(), opened: now()}
	fits, err := v.fits(ctx)
	if err != nil {
		return nil, fmt.Errorf("vault: %w", err)
	}
	v.locked.Store(!fits)
	return v, nil
}

// Get is the value of name in scope; "" when it is not stored. A row that
// does not decrypt is an error naming the scope and the name.
func (v *Vault) Get(ctx context.Context, scope secrets.Scope, name string) (string, error) {
	if secrets.Reserved(scope, name) {
		return "", fmt.Errorf("vault: get %s %s: %w", scope, name, ErrReserved)
	}
	return v.getCounted(ctx, scope, name)
}

// getCounted reads and counts one value.
func (v *Vault) getCounted(ctx context.Context, scope secrets.Scope, name string) (string, error) {
	value, out, err := v.get(ctx, scope, name)
	v.in.count(ctx, opGet, out)
	if err != nil {
		return "", fmt.Errorf("vault: get %s %s: %w", scope, name, err)
	}
	return value, nil
}

func (v *Vault) get(ctx context.Context, scope secrets.Scope, name string) (string, outcome, error) {
	if v.Locked() {
		return "", outcomeLocked, nil
	}
	var nonce, ciphertext []byte
	err := v.db.QueryRowContext(ctx,
		`SELECT nonce, ciphertext FROM secrets WHERE scope = ? AND name = ?`, string(scope), name).Scan(&nonce, &ciphertext)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", outcomeUnset, nil
	case err != nil:
		return "", outcomeError, err
	}
	plain, err := open(v.aead, scope, name, nonce, ciphertext)
	if err != nil {
		return "", outcomeError, err
	}
	return string(plain), outcomeOK, nil
}

// Set stores value, replacing an earlier one. by is who set it: "cli" or
// a principal.
func (v *Vault) Set(ctx context.Context, scope secrets.Scope, name, value, by string) error {
	if secrets.Reserved(scope, name) {
		return fmt.Errorf("vault: set %s %s: %w", scope, name, ErrReserved)
	}
	return v.setCounted(ctx, scope, name, value, by)
}

// setCounted stores and counts one value.
func (v *Vault) setCounted(ctx context.Context, scope secrets.Scope, name, value, by string) error {
	err := v.set(ctx, scope, name, value, by)
	v.in.count(ctx, opSet, setOutcome(err))
	if err != nil {
		return fmt.Errorf("vault: set %s %s: %w", scope, name, err)
	}
	return nil
}

func (v *Vault) set(ctx context.Context, scope secrets.Scope, name, value, by string) error {
	if v.Locked() {
		return ErrLocked
	}
	if err := check(name, value); err != nil {
		return err
	}
	nonce, ciphertext, err := seal(v.aead, scope, name, []byte(value))
	if err != nil {
		return err
	}
	_, err = v.db.ExecContext(ctx, `
		INSERT INTO secrets (scope, name, nonce, ciphertext, set_ms, set_by) VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (scope, name) DO UPDATE SET
			nonce = excluded.nonce, ciphertext = excluded.ciphertext,
			set_ms = excluded.set_ms, set_by = excluded.set_by`,
		string(scope), name, nonce, ciphertext, v.now().UnixMilli(), by)
	return err
}

// setOutcome is the outcome of a Set that returned err.
func setOutcome(err error) outcome {
	if errors.Is(err, ErrLocked) {
		return outcomeLocked
	}
	return done(err)
}

// check refuses a name or a value the store does not take.
func check(name, value string) error {
	switch {
	case !secrets.ValidName(name):
		return ErrName
	case value == "":
		return ErrEmpty
	case len(value) > MaxValue:
		return ErrTooLong
	}
	return nil
}

// Remove deletes one secret; false when there was none.
func (v *Vault) Remove(ctx context.Context, scope secrets.Scope, name string) (bool, error) {
	if secrets.Reserved(scope, name) {
		return false, fmt.Errorf("vault: remove %s %s: %w", scope, name, ErrReserved)
	}
	n, err := v.remove(ctx, `DELETE FROM secrets WHERE scope = ? AND name = ?`, string(scope), name)
	if err != nil {
		return false, fmt.Errorf("vault: remove %s %s: %w", scope, name, err)
	}
	return n > 0, nil
}

// RemoveScope deletes every secret of scope and returns how many. The
// node's scope holds its own keys, so it is refused.
func (v *Vault) RemoveScope(ctx context.Context, scope secrets.Scope) (int, error) {
	if scope == secrets.ScopeNode {
		return 0, fmt.Errorf("vault: remove %s: %w", scope, ErrReserved)
	}
	n, err := v.remove(ctx, `DELETE FROM secrets WHERE scope = ?`, string(scope))
	if err != nil {
		return 0, fmt.Errorf("vault: remove %s: %w", scope, err)
	}
	return n, nil
}

// remove runs a DELETE and returns how many rows it took.
func (v *Vault) remove(ctx context.Context, query string, args ...any) (int, error) {
	n, err := v.exec(ctx, query, args...)
	out := done(err)
	if err == nil && n == 0 {
		out = outcomeUnset
	}
	v.in.count(ctx, opRemove, out)
	return n, err
}

func (v *Vault) exec(ctx context.Context, query string, args ...any) (int, error) {
	res, err := v.db.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// List is every entry, by scope then name, without the node's own keys.
func (v *Vault) List(ctx context.Context) ([]Entry, error) {
	rows, err := v.db.QueryContext(ctx, `SELECT scope, name, set_ms, set_by FROM secrets ORDER BY scope, name`)
	if err != nil {
		return nil, fmt.Errorf("vault: list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Entry
	for rows.Next() {
		var (
			e     Entry
			setMS int64
		)
		if err := rows.Scan(&e.Scope, &e.Name, &setMS, &e.By); err != nil {
			return nil, fmt.Errorf("vault: list: %w", err)
		}
		if secrets.Reserved(e.Scope, e.Name) {
			continue
		}
		e.SetAt = time.UnixMilli(setMS)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("vault: list: %w", err)
	}
	return out, nil
}

// Internal is the store as the owner of the node's own keys sees it: it
// reads and writes only reserved names (secrets.Reserved), in the node's
// scope. The bus's trust chain is its one user.
type Internal struct{ v *Vault }

// Internal returns the store's view for the node's own keys.
func (v *Vault) Internal() Internal { return Internal{v: v} }

// Get implements natsbus.SeedStore.
func (in Internal) Get(ctx context.Context, scope secrets.Scope, name string) (string, error) {
	if !secrets.Reserved(scope, name) {
		return "", fmt.Errorf("vault: get %s %s: %w", scope, name, ErrName)
	}
	return in.v.getCounted(ctx, scope, name)
}

// Set implements natsbus.SeedStore.
func (in Internal) Set(ctx context.Context, scope secrets.Scope, name, value, by string) error {
	if !secrets.Reserved(scope, name) {
		return fmt.Errorf("vault: set %s %s: %w", scope, name, ErrName)
	}
	return in.v.setCounted(ctx, scope, name, value, by)
}

// Locked reports whether the store is locked.
func (in Internal) Locked() bool { return in.v.Locked() }

// scoped reads one scope of the store as a secrets.Source.
type scoped struct {
	v     *Vault
	scope secrets.Scope
}

// Secret implements secrets.Source.
func (s scoped) Secret(name string) (string, error) {
	return s.v.Get(context.Background(), s.scope, name)
}

// Source reads scope as a secrets.Source, for the code that takes one.
// secrets.Source has no context, and a read of one row needs no deadline
// beyond SQLite's busy timeout, so the read runs on the background context.
func (v *Vault) Source(scope secrets.Scope) secrets.Source { return scoped{v: v, scope: scope} }

// Has reports whether a row exists, without a key: for a read-only
// database (ganglion mcp). A database without the table has none.
func Has(ctx context.Context, db *sql.DB, scope secrets.Scope, name string) (bool, error) {
	var has bool
	err := db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = 'secrets')`).Scan(&has)
	if err != nil || !has {
		return false, wrapHas(scope, name, err)
	}
	err = db.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM secrets WHERE scope = ? AND name = ?)`, string(scope), name).Scan(&has)
	return has && err == nil, wrapHas(scope, name, err)
}

func wrapHas(scope secrets.Scope, name string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("vault: has %s %s: %w", scope, name, err)
}
