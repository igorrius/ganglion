// Package modstore installs, updates and rolls back the node's modules
// without a restart (R-RUN-8, ADR-0041). Built-in modules ship in the
// binary; a release from the project's channel replaces one only after
// its sum, its manifest against a ceiling the core embeds, and a
// self-test on built-in samples all pass. The active and the previous
// version are kept on the data volume; the switch is atomic and calls in
// flight finish on the old version.
package modstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/sqlitedb"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

//go:embed migrations/*.sql
var migrations embed.FS

// SourceBuiltin is the source and version of a module's embedded build.
const SourceBuiltin = "builtin"

// State is where a version stands.
type State string

// States of a version.
const (
	StateActive   State = "active"
	StatePrevious State = "previous" // kept for a rollback
	StateFailed   State = "failed"   // refused, or failed its check at start
	StateRetired  State = "retired"  // replaced; its files are gone
)

// UpdateResult is how an update or a rollback ended.
type UpdateResult string

// Results.
const (
	ResultInstalled         UpdateResult = "installed"
	ResultUpToDate          UpdateResult = "up_to_date"
	ResultRefused           UpdateResult = "refused"
	ResultFailedSelfTest    UpdateResult = "failed_selftest"
	ResultRolledBack        UpdateResult = "rolled_back"
	ResultNothingToRollBack UpdateResult = "nothing_to_roll_back"
)

// ErrUnknownModule is a module the node does not have.
var ErrUnknownModule = errors.New("modstore: unknown module")

// Ceiling is the most a release of a built-in module may ask for; only a
// new core raises it. A tool module gets no capability at all.
type Ceiling struct {
	ABI        int
	MaxMemory  uint64
	MaxTimeout time.Duration
}

// SelfTest checks a candidate version through its call function.
type SelfTest func(ctx context.Context, call func(context.Context, json.RawMessage) (json.RawMessage, error)) error

// Builtin is a module embedded in the binary.
type Builtin struct {
	Name           string
	Wasm, Manifest []byte
	Ceiling        Ceiling
	SelfTest       SelfTest
}

// Release is a version a Source offers.
type Release struct {
	Module, Version, Tag   string
	WasmURL, ManifestURL   string
	SHA256, ManifestSHA256 string // from the release's SHA256SUMS
}

// Source finds and fetches releases (the project's GitHub Releases).
type Source interface {
	// Latest is the newest release of module whose ABI this core speaks.
	Latest(ctx context.Context, module string, abi int) (Release, error)
	Fetch(ctx context.Context, r Release) (wasm, manifest []byte, err error)
}

// Outcome is what an update or a rollback did, in words for the owner.
type Outcome struct {
	Module, From, To string
	Result           UpdateResult
	Reason           string
}

// Version is one row of a module's versions.
type Version struct {
	Module, Version, Source, SHA256 string
	State                           State
	Reason                          string
	Installed                       time.Time
	manifestSHA256                  string
}

// Store holds the modules' versions.
type Store struct {
	db   *sql.DB
	dir  string
	now  func() time.Time
	deps runner.Deps

	builtins map[string]Builtin
	tools    map[string]*runner.SwapTool

	mu       sync.Mutex          // one update or rollback at a time
	activeMu sync.Mutex          // the gauge reads active without waiting for an update
	active   map[string]string   // module → source of the active version, for the gauge
	fellBack map[string]fallback // modules whose stored version failed its check at start, for /status
	log      *slog.Logger
	links    LinksFunc // the node file's links, which a build is checked against

	updates  metric.Int64Counter
	selftest metric.Float64Histogram
}

// Open migrates the store, loads each built-in module's active version
// (the embedded build when none is stored, or when the stored files fail
// their sum) and returns the store.
func Open(ctx context.Context, db *sql.DB, dir string, now func() time.Time, deps runner.Deps, bs ...Builtin) (*Store, error) {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return nil, err
	}
	if err := sqlitedb.Migrate(ctx, db, "modstore", sub); err != nil {
		return nil, err
	}
	s := &Store{db: db, dir: dir, now: now, deps: deps, builtins: map[string]Builtin{}, tools: map[string]*runner.SwapTool{},
		active: map[string]string{}, fellBack: map[string]fallback{}, log: deps.Logger, links: NoLinks}
	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}
	if err := s.instruments(); err != nil {
		return nil, err
	}
	for _, b := range bs {
		s.builtins[b.Name] = b
		tool, src, err := s.load(ctx, b)
		if err != nil {
			return nil, err
		}
		s.tools[b.Name], s.active[b.Name] = runner.NewSwapTool(tool), src
		if err := s.sweep(ctx, b.Name); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func (s *Store) instruments() error {
	m := otel.Meter("ganglion/modstore")
	s.updates, _ = m.Int64Counter("ganglion.module.updates", metric.WithDescription("Module updates and rollbacks, by module and result."))
	s.selftest, _ = m.Float64Histogram("ganglion.module.selftest.duration", metric.WithUnit("s"),
		metric.WithDescription("How long a candidate version's self-test took."))
	_, err := m.Int64ObservableGauge("ganglion.module.active", metric.WithDescription("1 for each module's active version, by its source."),
		metric.WithInt64Callback(func(_ context.Context, o metric.Int64Observer) error {
			s.activeMu.Lock()
			defer s.activeMu.Unlock()
			for mod, src := range s.active {
				o.Observe(1, metric.WithAttributes(attribute.String("module", mod), attribute.String("source", src)))
			}
			return nil
		}))
	return err
}

// Tool is the module's switchable tool.
func (s *Store) Tool(name string) (*runner.SwapTool, bool) {
	t, ok := s.tools[name]
	return t, ok
}

func sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// load picks a module's active version at start. Two active rows (a
// crash, an old bug) resolve to the newest; a stored version whose files
// are missing or do not match its sum is marked failed, and the embedded
// build becomes active.
func (s *Store) load(ctx context.Context, b Builtin) (*runner.LazyTool, string, error) {
	rows, err := s.versions(ctx, b.Name, StateActive)
	if err != nil {
		return nil, "", err
	}
	for i, v := range rows {
		if i > 0 { // older duplicates
			if err := s.setState(ctx, b.Name, v.SHA256, StatePrevious, "two active versions at start; the newest kept"); err != nil {
				return nil, "", err
			}
		}
	}
	if len(rows) > 0 && rows[0].Source != SourceBuiltin {
		v := rows[0]
		tool, err := s.stored(b, v)
		if err == nil {
			return tool, v.Source, nil
		}
		s.log.Warn("modstore: the stored version fails its check; the built-in build runs", "module", b.Name, "version", v.Version, "err", err)
		s.noteFallback(b.Name, v.Version)
		if err := s.setState(ctx, b.Name, v.SHA256, StateFailed, "at start: "+reasonOf(err)); err != nil {
			return nil, "", err
		}
	}
	if err := s.activateBuiltin(ctx, b); err != nil {
		return nil, "", err
	}
	tool, err := runner.NewLazyTool(b.Wasm, b.Manifest, s.deps)
	return tool, SourceBuiltin, err
}

// activateBuiltin makes the embedded build the active row (its sum may
// have changed with a new core).
func (s *Store) activateBuiltin(ctx context.Context, b Builtin) error {
	return sqlitedb.Immediate(ctx, s.db, func(c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, `DELETE FROM module_versions WHERE module = ? AND source = ? AND state = 'active'`,
			b.Name, SourceBuiltin); err != nil {
			return err
		}
		_, err := c.ExecContext(ctx, `INSERT INTO module_versions (module, sha256, version, source, state, installed_ms)
			VALUES (?, ?, ?, ?, 'active', ?)
			ON CONFLICT (module, sha256) DO UPDATE SET state = 'active', version = excluded.version, source = excluded.source, reason = ''`,
			b.Name, sum(b.Wasm), SourceBuiltin, SourceBuiltin, s.now().UnixMilli())
		return err
	})
}

// stored reads a stored version, checks both files against their sums
// and its manifest against this core's ceiling, and prepares it.
func (s *Store) stored(b Builtin, v Version) (*runner.LazyTool, error) {
	base := filepath.Join(s.dir, b.Name, v.SHA256)
	wasm, err := os.ReadFile(base + ".wasm")
	if err != nil {
		return nil, &refusal{result: ResultRefused, reason: "the stored files cannot be read", detail: err.Error()}
	}
	manifest, err := os.ReadFile(base + ".yaml")
	if err != nil {
		return nil, &refusal{result: ResultRefused, reason: "the stored files cannot be read", detail: err.Error()}
	}
	if sum(wasm) != v.SHA256 || sum(manifest) != v.manifestSHA256 {
		return nil, refuse("the stored files do not match their sums")
	}
	if err := checkManifest(b, manifest); err != nil {
		return nil, err
	}
	tool, err := runner.NewLazyTool(wasm, manifest, s.deps)
	if err != nil {
		return nil, &refusal{result: ResultRefused, reason: "the module could not be prepared", detail: err.Error()}
	}
	return tool, nil
}

// sweep removes from the module's directory every file that is not the
// active or the previous version's: what a crash left (a .tmp, a version
// written but never recorded, a retired one not yet removed).
func (s *Store) sweep(ctx context.Context, module string) error {
	keep := map[string]bool{}
	for _, st := range []State{StateActive, StatePrevious} {
		vs, err := s.versions(ctx, module, st)
		if err != nil {
			return err
		}
		for _, v := range vs {
			keep[v.SHA256+".wasm"], keep[v.SHA256+".yaml"] = true, true
		}
	}
	dir := filepath.Join(s.dir, module)
	es, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("modstore: %w", err)
	}
	for _, e := range es {
		if e.Type().IsRegular() && !keep[e.Name()] {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
	return nil
}

// versions are a module's rows in a state, newest first.
func (s *Store) versions(ctx context.Context, module string, st State) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT module, version, source, sha256, manifest_sha256, state, reason, installed_ms FROM module_versions
		WHERE module = ? AND state = ? ORDER BY installed_ms DESC, rowid DESC`, module, st)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

func scan(rows *sql.Rows) ([]Version, error) {
	defer func() { _ = rows.Close() }()
	var out []Version
	for rows.Next() {
		var (
			v  Version
			ms int64
		)
		if err := rows.Scan(&v.Module, &v.Version, &v.Source, &v.SHA256, &v.manifestSHA256, &v.State, &v.Reason, &ms); err != nil {
			return nil, err
		}
		v.Installed = time.UnixMilli(ms).UTC()
		out = append(out, v)
	}
	return out, rows.Err()
}

// List is every module's versions that are not retired, active first.
func (s *Store) List(ctx context.Context) ([]Version, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT module, version, source, sha256, manifest_sha256, state, reason, installed_ms FROM module_versions
		WHERE state != 'retired'
		ORDER BY module, CASE state WHEN 'active' THEN 0 WHEN 'previous' THEN 1 ELSE 2 END, installed_ms DESC`)
	if err != nil {
		return nil, err
	}
	return scan(rows)
}

func (s *Store) setState(ctx context.Context, module, sha string, st State, reason string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE module_versions SET state = ?, reason = ? WHERE module = ? AND sha256 = ?`,
		st, reason, module, sha)
	return err
}

// Shutdown frees every module's current version.
func (s *Store) Shutdown(ctx context.Context) error {
	var errs []error
	for _, t := range s.tools {
		errs = append(errs, t.Shutdown(ctx))
	}
	return errors.Join(errs...)
}

// Admin is the store with its release source: what /modules drives.
type Admin struct {
	Store  *Store
	Source Source
}

// List lists the versions.
func (a Admin) List(ctx context.Context) ([]Version, error) { return a.Store.List(ctx) }

// Update installs the newest release of name from the source.
func (a Admin) Update(ctx context.Context, name string) (Outcome, error) {
	return a.Store.Update(ctx, name, a.Source)
}

// Rollback returns name to its previous version.
func (a Admin) Rollback(ctx context.Context, name string) (Outcome, error) {
	return a.Store.Rollback(ctx, name)
}
