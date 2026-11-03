package modstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/sqlitedb"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// refusal is a check a candidate failed: the update keeps the active
// version and says why.
type refusal struct {
	result UpdateResult
	reason string
}

func (r *refusal) Error() string { return r.reason }

func refuse(format string, a ...any) error {
	return &refusal{result: ResultRefused, reason: fmt.Sprintf(format, a...)}
}

// Update installs the newest release of module that passes every check
// (ADR-0041): the sum, the manifest against the ceiling, the self-test.
// A refusal keeps the active version and is an Outcome, not an error;
// errors are the store's own (the database, the disk).
func (s *Store) Update(ctx context.Context, module string, src Source) (Outcome, error) {
	b, ok := s.builtins[module]
	if !ok {
		return Outcome{}, fmt.Errorf("%w %q", ErrUnknownModule, module)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.current(ctx, module)
	if err != nil {
		return Outcome{}, err
	}
	o := Outcome{Module: module, From: cur.Version}
	rel, tool, err := s.candidate(ctx, b, cur, src)
	var r *refusal
	switch {
	case errors.As(err, &r):
		o.Result, o.Reason, o.To = r.result, r.reason, rel.Version
		if r.result == ResultUpToDate {
			o.To = cur.Version
		}
		s.count(ctx, module, o.Result)
		return o, s.recordFailure(ctx, module, rel, r)
	case err != nil:
		return Outcome{}, err
	}
	if err := s.install(ctx, module, rel, tool); err != nil {
		_ = tool.Shutdown(ctx)
		return Outcome{}, err
	}
	o.Result, o.To = ResultInstalled, rel.Version
	s.count(ctx, module, o.Result)
	return o, nil
}

func (s *Store) count(ctx context.Context, module string, r UpdateResult) {
	s.updates.Add(ctx, 1, metric.WithAttributes(attribute.String("module", module), attribute.String("result", string(r))))
}

// current is the module's active row.
func (s *Store) current(ctx context.Context, module string) (Version, error) {
	vs, err := s.versions(ctx, module, StateActive)
	if err != nil || len(vs) == 0 {
		return Version{}, errors.Join(err, errors.New("modstore: no active version of "+module))
	}
	return vs[0], nil
}

// candidate finds, fetches and checks the newest release; it returns the
// release and its compiled, self-tested tool, or a *refusal.
func (s *Store) candidate(ctx context.Context, b Builtin, cur Version, src Source) (Release, *runner.LazyTool, error) {
	rel, err := src.Latest(ctx, b.Name, b.Ceiling.ABI)
	if err != nil {
		return rel, nil, refuse("no release found: %v", err)
	}
	if rel.SHA256 == cur.SHA256 {
		return rel, nil, &refusal{result: ResultUpToDate, reason: "the active version is the newest release"}
	}
	wasm, manifest, err := src.Fetch(ctx, rel)
	if err != nil {
		return rel, nil, refuse("download failed: %v", err)
	}
	if sum(wasm) != rel.SHA256 || sum(manifest) != rel.ManifestSHA256 {
		return rel, nil, refuse("the files do not match the release's sums")
	}
	if err := checkManifest(b, manifest); err != nil {
		return rel, nil, err
	}
	tool, err := runner.NewLazyTool(wasm, manifest, s.deps)
	if err != nil {
		return rel, nil, refuse("manifest: %v", err) // a capability: ErrCapabilities
	}
	started := time.Now()
	err = b.SelfTest(ctx, tool.Call)
	s.selftest.Record(ctx, time.Since(started).Seconds(), metric.WithAttributes(attribute.String("module", b.Name)))
	if err != nil {
		_ = tool.Shutdown(ctx)
		return rel, nil, &refusal{result: ResultFailedSelfTest, reason: fmt.Sprintf("the self-test failed: %v", err)}
	}
	if err := writeFiles(filepath.Join(s.dir, b.Name), rel.SHA256, wasm, manifest); err != nil {
		_ = tool.Shutdown(ctx)
		return rel, nil, err
	}
	return rel, tool, nil
}

// checkManifest holds a release's manifest against the module's ceiling:
// its name, its ABI, and its limits. Capabilities are refused by the
// runner for every tool module.
func checkManifest(b Builtin, src []byte) error {
	m, err := runner.ParseManifest(src)
	switch {
	case err != nil:
		return refuse("manifest: %v", err)
	case m.Name != b.Name:
		return refuse("the manifest's name is %q, not %q", m.Name, b.Name)
	case m.ABI != b.Ceiling.ABI:
		return refuse("the module speaks ABI %d; this core speaks %d (a new core is needed)", m.ABI, b.Ceiling.ABI)
	case m.Limits.Memory > b.Ceiling.MaxMemory:
		return refuse("it asks for %d MiB of memory, over the ceiling of %d MiB", m.Limits.Memory>>20, b.Ceiling.MaxMemory>>20)
	case m.Limits.Timeout > b.Ceiling.MaxTimeout:
		return refuse("it asks for a timeout of %s, over the ceiling of %s", m.Limits.Timeout, b.Ceiling.MaxTimeout)
	}
	return nil
}

// writeFiles stores a version: each file written to a temporary name and
// renamed, so a crash never leaves half a module under its sum.
func writeFiles(dir, sha string, wasm, manifest []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("modstore: %w", err)
	}
	for ext, data := range map[string][]byte{".wasm": wasm, ".yaml": manifest} {
		final := filepath.Join(dir, sha+ext)
		tmp := final + ".tmp"
		if err := os.WriteFile(tmp, data, 0o600); err != nil {
			return fmt.Errorf("modstore: %w", err)
		}
		if err := os.Rename(tmp, final); err != nil {
			return fmt.Errorf("modstore: %w", err)
		}
	}
	return nil
}

// recordFailure keeps a failed candidate's row with its reason, so
// /modules shows why. An up-to-date check records nothing.
func (s *Store) recordFailure(ctx context.Context, module string, rel Release, r *refusal) error {
	if r.result == ResultUpToDate || rel.SHA256 == "" {
		return nil
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO module_versions (module, sha256, version, source, state, reason, installed_ms)
		VALUES (?, ?, ?, ?, 'failed', ?, ?)
		ON CONFLICT (module, sha256) DO UPDATE SET reason = excluded.reason
		WHERE module_versions.state IN ('failed', 'retired')`,
		module, rel.SHA256, rel.Version, rel.Tag, r.reason, s.now().UnixMilli())
	return err
}

// install records the new version as active, the old active as previous
// and retires the version before it, in one transaction; then it swaps
// the tool and removes the retired files.
func (s *Store) install(ctx context.Context, module string, rel Release, tool *runner.LazyTool) error {
	var retired []string
	err := sqlitedb.Immediate(ctx, s.db, func(c *sql.Conn) error {
		var err error
		if retired, err = retire(ctx, c, module, StatePrevious); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, `UPDATE module_versions SET state = 'previous' WHERE module = ? AND state = 'active'`, module); err != nil {
			return err
		}
		_, err = c.ExecContext(ctx, `INSERT INTO module_versions (module, sha256, version, source, state, installed_ms)
			VALUES (?, ?, ?, ?, 'active', ?)
			ON CONFLICT (module, sha256) DO UPDATE SET state = 'active', version = excluded.version, source = excluded.source,
				reason = '', installed_ms = excluded.installed_ms`,
			module, rel.SHA256, rel.Version, rel.Tag, s.now().UnixMilli())
		return err
	})
	if err != nil {
		return fmt.Errorf("modstore: %w", err)
	}
	s.swap(module, rel.Tag, tool)
	s.removeFiles(module, retired)
	return nil
}

// retire marks the module's rows in state st retired and returns their
// sums.
func retire(ctx context.Context, c *sql.Conn, module string, st State) ([]string, error) {
	rows, err := c.QueryContext(ctx, `SELECT sha256 FROM module_versions WHERE module = ? AND state = ?`, module, st)
	if err != nil {
		return nil, err
	}
	var shas []string
	for rows.Next() {
		var sha string
		if err := rows.Scan(&sha); err != nil {
			_ = rows.Close()
			return nil, err
		}
		shas = append(shas, sha)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	_, err = c.ExecContext(ctx, `UPDATE module_versions SET state = 'retired', reason = 'replaced' WHERE module = ? AND state = ?`, module, st)
	return shas, err
}

func (s *Store) swap(module, source string, tool runner.Swappable) {
	_ = s.tools[module].Swap(tool)
	s.active[module] = source
}

func (s *Store) removeFiles(module string, shas []string) {
	for _, sha := range shas {
		for _, ext := range []string{".wasm", ".yaml"} {
			_ = os.Remove(filepath.Join(s.dir, module, sha+ext)) // the built-in build has none
		}
	}
}

// Rollback makes the previous version active again and retires the one
// it replaces: a second rollback has nothing to go back to.
func (s *Store) Rollback(ctx context.Context, module string) (Outcome, error) {
	b, ok := s.builtins[module]
	if !ok {
		return Outcome{}, fmt.Errorf("%w %q", ErrUnknownModule, module)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.current(ctx, module)
	if err != nil {
		return Outcome{}, err
	}
	prevs, err := s.versions(ctx, module, StatePrevious)
	if err != nil {
		return Outcome{}, err
	}
	if len(prevs) == 0 {
		s.count(ctx, module, ResultNothingToRollBack)
		return Outcome{Module: module, From: cur.Version, To: cur.Version, Result: ResultNothingToRollBack,
			Reason: "no previous version is kept"}, nil
	}
	prev := prevs[0]
	wasm, manifest := b.Wasm, b.Manifest
	if prev.Source != SourceBuiltin {
		if wasm, manifest, err = s.readFiles(module, prev.SHA256); err != nil {
			return Outcome{Module: module, From: cur.Version, To: prev.Version, Result: ResultRefused,
				Reason: fmt.Sprintf("the previous version cannot be read: %v", err)}, nil
		}
	}
	tool, err := runner.NewLazyTool(wasm, manifest, s.deps)
	if err != nil {
		return Outcome{}, err
	}
	var retired []string
	err = sqlitedb.Immediate(ctx, s.db, func(c *sql.Conn) error {
		var err error
		if retired, err = retire(ctx, c, module, StateActive); err != nil {
			return err
		}
		_, err = c.ExecContext(ctx, `UPDATE module_versions SET state = 'active', reason = '' WHERE module = ? AND sha256 = ?`,
			module, prev.SHA256)
		return err
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("modstore: %w", err)
	}
	s.swap(module, prev.Source, tool)
	s.removeFiles(module, retired)
	s.count(ctx, module, ResultRolledBack)
	return Outcome{Module: module, From: cur.Version, To: prev.Version, Result: ResultRolledBack}, nil
}
