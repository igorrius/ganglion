package modstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/sqlitedb"
)

// SourceLocal is the source of a version built from the workspace.
const SourceLocal = "local"

// LocalPrefix starts the name of every module built on this node (ADR-0061).
const LocalPrefix = "local/"

// Reserved lists names a local module may not take (ADR-0062): they are the
// prefixes of the node's own tools and cells.
var Reserved = []string{"html2md", "cells", "module", "modules", "memory", "host", "web", "schedule", "message", "skill", "metrics", "config", "request", "tools"}

// ErrBuildMismatch is an install of bytes that are not the build the plan
// named: the press installs exactly the sha it showed.
var ErrBuildMismatch = errors.New("modstore: this is not the build the install window showed (its digest differs)")

// Digest is the identity of a build: sha256 over the 32-byte sha256 of the
// .wasm followed by the 32-byte sha256 of the manifest, as lower-case hex.
// It covers both files, so the rights the window shows (read from the
// manifest) and the code that runs cannot differ, and two builds with the
// same wasm and different manifests never share a name on disk.
func Digest(wasm, manifest []byte) string {
	w, m := sha256.Sum256(wasm), sha256.Sum256(manifest)
	d := sha256.Sum256(append(w[:], m[:]...))
	return hex.EncodeToString(d[:])
}

var shortNameRE = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// wasmMagic starts every WebAssembly binary.
var wasmMagic = []byte("\x00asm")

// Build is what the builder made of a module's source.
type Build struct {
	Module   string // "local/deye-reader"
	Wasm     []byte
	Manifest []byte
	Language string
	By       string // principal who asked
	Note     string // the agent's or owner's words
	Commit   string // workspace commit the source was built from
}

// Rights is what the install window shows: everything the module may do.
type Rights struct {
	Hosts, Secrets, CellsCreated, CellsTaken, CellsRead, Crons, Tools []string
	Memory                                                            uint64
	Timeout                                                           time.Duration
}

// Plan is a build checked but not installed.
type Plan struct {
	SHA256   string // the build's Digest (wasm and manifest); what InstallLocal takes
	Version  int    // the manifest's version
	Size     int
	Rights   Rights
	Widening Rights // what the active version did not have; empty for a first install
	Refusal  string // "" = installable; else the node's reason, in the node's fixed words
	// RefusalDetail is text taken from the build's source (the parser's
	// message, a name that is not plain): it may quote the manifest. It is
	// shown to the owner, and to the model only as outside content.
	RefusalDetail string
}

// CellOwners reports a cell's owner ("" = ownerless) and whether it exists.
type CellOwners func(ctx context.Context, id string) (owner string, exists bool, err error)

// Active is an installed local module's active version.
type Active struct {
	Module, SHA256 string
	Wasm, Manifest []byte
}

// IsLocal reports whether name is the name of a local module.
func IsLocal(name string) bool { return strings.HasPrefix(name, LocalPrefix) }

// ShortName is the part of a local module's name after local/: the prefix of
// its cells.
func ShortName(name string) string { return strings.TrimPrefix(name, LocalPrefix) }

// checkBuild is everything about a build that needs neither the node's
// secrets nor its cells: the name, the bytes and the manifest. A reason is
// the node's own words.
func checkBuild(b Build) (runner.Manifest, string, string) {
	short := ShortName(b.Module)
	switch {
	case !IsLocal(b.Module):
		return runner.Manifest{}, "a module built on this node is named local/<name>", ""
	case !shortNameRE.MatchString(short):
		return runner.Manifest{}, "the name after local/ is lower-case letters, digits, - and _, starting with a letter", ""
	case slices.Contains(Reserved, short):
		return runner.Manifest{}, fmt.Sprintf("the name %s is reserved for the node's own tools", short), ""
	case !bytes.HasPrefix(b.Wasm, wasmMagic):
		return runner.Manifest{}, "the build is not a WebAssembly module", ""
	}
	m, err := runner.ParseManifest(b.Manifest)
	switch {
	case err != nil:
		return m, "the manifest is not valid for this core", text.Truncate(err.Error(), 300)
	case m.Name != short && m.Name != b.Module:
		return m, "the manifest names another module", ""
	case m.ABI != runner.ABIVersion:
		return m, fmt.Sprintf("the module speaks ABI %d; this core speaks %d", m.ABI, runner.ABIVersion), ""
	case len(m.Capabilities.EventEmit) > 0:
		return m, "the manifest asks for event.emit; a local module may not emit events", ""
	}
	for _, t := range m.Triggers {
		if t.Tool != "" && t.Output == nil {
			return m, fmt.Sprintf("the tool %s has no output: its answer would reach the model unchecked", t.Tool), ""
		}
	}
	return m, "", ""
}

// PlanLocal checks a build without installing it: the name is local/…, not
// reserved, the manifest parses, every secret is set (via the given
// lookup), every required config key is given (from the module's settings),
// no cell belongs to another owner. A refusal is in Plan.Refusal and names
// the cause, never a value; an error is the store's own.
func (s *Store) PlanLocal(ctx context.Context, b Build, secretSet func(string) bool, config map[string]any, owners CellOwners) (Plan, error) {
	p := Plan{SHA256: Digest(b.Wasm, b.Manifest), Size: len(b.Wasm)}
	m, why, detail := checkBuild(b)
	if why != "" {
		p.Refusal, p.RefusalDetail = why, detail
		return p, nil
	}
	p.Version = m.Version
	if p.Refusal, p.RefusalDetail = missing(m, secretSet, config); p.Refusal != "" {
		return p, nil
	}
	rights, why, err := cellRights(ctx, b.Module, m, owners)
	if err != nil || why != "" {
		p.Refusal = why
		return p, err
	}
	p.Rights = rights
	old, ok, err := s.activeRights(ctx, b.Module)
	if err != nil {
		return p, err
	}
	if ok {
		p.Widening = widening(rights, old)
	}
	return p, nil
}

// plainName is a name safe to put in the node's own words: short and made
// of the characters names are made of. Anything else is detail.
var plainNameRE = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)

// named is the refusal "<what> NAME is <rest>" with the name in the node's
// words when it is a plain name; otherwise the words stay fixed and the name
// goes to the detail.
func named(what, name, rest string) (string, string) {
	if plainNameRE.MatchString(name) {
		return fmt.Sprintf("%s %s %s", what, name, rest), ""
	}
	return fmt.Sprintf("a %s the module names %s", what, rest), text.Truncate(name, 100)
}

// missing names the first secret that is not set or required config key
// that is not given, as a refusal and its detail.
func missing(m runner.Manifest, secretSet func(string) bool, config map[string]any) (string, string) {
	for _, name := range sorted(m.Capabilities.SecretGet) {
		if !secretSet(name) {
			return named("the secret", name, "is not set on this node")
		}
	}
	for _, key := range sortedKeys(m.Config) {
		k := m.Config[key]
		if _, given := config[key]; k.Required && k.Default == "" && !given {
			return named("the config key", key, "is required and not set in the module's settings")
		}
	}
	return "", ""
}

// cellRights splits the cells a module declares into those it would create,
// those it would take over (they exist, ownerless) and those it already owns
// (an update, no new right); a cell of another owner is a refusal.
func cellRights(ctx context.Context, module string, m runner.Manifest, owners CellOwners) (Rights, string, error) {
	r := baseRights(m)
	r.CellsCreated = nil
	for _, id := range cellIDs(module, m) {
		owner, exists, err := owners(ctx, id)
		switch {
		case err != nil:
			return Rights{}, "", fmt.Errorf("modstore: cell %s: %w", id, err)
		case !exists:
			r.CellsCreated = append(r.CellsCreated, id)
		case owner == "":
			r.CellsTaken = append(r.CellsTaken, id)
		case owner != module:
			return Rights{}, fmt.Sprintf("the cell %s belongs to %s", id, owner), nil
		}
	}
	return r, "", nil
}

// cellIDs are the ids of the cells a module declares, sorted.
func cellIDs(module string, m runner.Manifest) []string {
	var ids []string
	for name := range m.Cells.Report {
		ids = append(ids, ShortName(module)+"."+name)
	}
	slices.Sort(ids)
	return ids
}

// baseRights are the rights a manifest states outright; every declared cell
// is counted as created until the cells are looked up.
func baseRights(m runner.Manifest) Rights {
	r := Rights{
		Hosts: sorted(m.Capabilities.HTTPFetch), Secrets: sorted(m.Capabilities.SecretGet),
		CellsRead: sorted(m.Cells.Read), Memory: m.Limits.Memory, Timeout: m.Limits.Timeout,
	}
	for _, t := range m.Triggers {
		if t.Cron != "" {
			r.Crons = append(r.Crons, t.Cron)
		}
		if t.Tool != "" {
			r.Tools = append(r.Tools, t.Tool)
		}
	}
	r.Crons, r.Tools = sorted(r.Crons), sorted(r.Tools)
	for name := range m.Cells.Report {
		r.CellsCreated = append(r.CellsCreated, name)
	}
	r.CellsCreated = sorted(r.CellsCreated)
	return r
}

// activeRights are the rights of the module's active version; false when
// it has none (a first install widens nothing). A version whose files cannot
// be read counts as having no rights, so everything shows as widening; a
// database that fails is an error, never a first install.
func (s *Store) activeRights(ctx context.Context, module string) (activeRightsSet, bool, error) {
	vs, err := s.versions(ctx, module, StateActive)
	if err != nil {
		return activeRightsSet{}, false, fmt.Errorf("modstore: %w", err)
	}
	if len(vs) == 0 {
		return activeRightsSet{}, false, nil
	}
	_, manifest, err := s.readLocal(vs[0])
	if err != nil {
		return activeRightsSet{}, true, nil
	}
	m, err := runner.ParseManifest(manifest)
	if err != nil {
		return activeRightsSet{}, true, nil
	}
	return activeRightsSet{Rights: baseRights(m), cells: cellIDs(module, m)}, true, nil
}

// activeRightsSet is the old version's rights and the cells it declared.
type activeRightsSet struct {
	Rights
	cells []string
}

// widening is what next has and old had not. A cell the old version
// declared is not new, whether it is created or taken now.
func widening(next Rights, old activeRightsSet) Rights {
	return Rights{
		Hosts: minus(next.Hosts, old.Hosts), Secrets: minus(next.Secrets, old.Secrets),
		CellsCreated: minus(next.CellsCreated, old.cells), CellsTaken: minus(next.CellsTaken, old.cells),
		CellsRead: minus(next.CellsRead, old.CellsRead), Crons: minus(next.Crons, old.Crons), Tools: minus(next.Tools, old.Tools),
		Memory: wider(next.Memory, old.Memory), Timeout: wider(next.Timeout, old.Timeout),
	}
}

func wider[T uint64 | time.Duration](next, old T) T {
	if next > old {
		return next
	}
	return 0
}

func minus(a, b []string) []string {
	var out []string
	for _, x := range a {
		if !slices.Contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func sorted(in []string) []string {
	out := slices.Clone(in)
	slices.Sort(out)
	return slices.Compact(out)
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// InstallLocal stores and activates exactly the build whose digest the plan
// showed: a wasm and a manifest whose Digest is anything else are
// ErrBuildMismatch, nothing is stored. The build
// is checked again for what needs no node state (a refusal is an Outcome);
// then, in one transaction, the old active becomes previous and the one
// before it retired with its files removed (its journal row stays).
func (s *Store) InstallLocal(ctx context.Context, b Build, sha string) (Outcome, error) {
	if Digest(b.Wasm, b.Manifest) != sha {
		return Outcome{}, ErrBuildMismatch
	}
	m, why, _ := checkBuild(b)
	if why != "" {
		return Outcome{Module: b.Module, Result: ResultRefused, Reason: why}, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	from := ""
	if cur, err := s.versions(ctx, b.Module, StateActive); err != nil {
		return Outcome{}, fmt.Errorf("modstore: %w", err)
	} else if len(cur) > 0 {
		from = cur[0].Version
		if cur[0].SHA256 == sha {
			return Outcome{Module: b.Module, From: from, To: from, Result: ResultUpToDate, Reason: "this build is already active"}, nil
		}
	}
	if err := writeFiles(filepath.Join(s.dir, b.Module), sha, b.Wasm, b.Manifest); err != nil {
		return Outcome{}, err
	}
	var retired []string
	err := sqlitedb.Immediate(ctx, s.db, func(c *sql.Conn) error {
		var err error
		if retired, err = retire(ctx, c, b.Module, StatePrevious, sha); err != nil {
			return err
		}
		if _, err := c.ExecContext(ctx, `UPDATE module_versions SET state = 'previous' WHERE module = ? AND state = 'active'`, b.Module); err != nil {
			return err
		}
		_, err = c.ExecContext(ctx, `INSERT INTO module_versions (module, sha256, manifest_sha256, version, source, state, installed_ms,
				"by", note, commit_id, language)
			VALUES (?, ?, ?, ?, ?, 'active', ?, ?, ?, ?, ?)
			ON CONFLICT (module, sha256) DO UPDATE SET state = 'active', manifest_sha256 = excluded.manifest_sha256,
				version = excluded.version, source = excluded.source, reason = '', installed_ms = excluded.installed_ms,
				"by" = excluded."by", note = excluded.note, commit_id = excluded.commit_id, language = excluded.language`,
			b.Module, sha, sum(b.Manifest), strconv.Itoa(m.Version), SourceLocal, s.now().UnixMilli(), b.By, b.Note, b.Commit, b.Language)
		return err
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("modstore: %w", err)
	}
	s.removeFiles(b.Module, retired)
	s.count(ctx, b.Module, ResultInstalled)
	return Outcome{Module: b.Module, From: from, To: strconv.Itoa(m.Version), Result: ResultInstalled}, nil
}

// readLocal reads a stored local version and checks both files against
// their sums.
func (s *Store) readLocal(v Version) (wasm, manifest []byte, err error) {
	base := filepath.Join(s.dir, v.Module, v.SHA256)
	if wasm, err = os.ReadFile(base + ".wasm"); err != nil {
		return nil, nil, fmt.Errorf("modstore: the stored files cannot be read: %w", err)
	}
	if manifest, err = os.ReadFile(base + ".yaml"); err != nil {
		return nil, nil, fmt.Errorf("modstore: the stored files cannot be read: %w", err)
	}
	if Digest(wasm, manifest) != v.SHA256 || sum(manifest) != v.manifestSHA256 {
		return nil, nil, errors.New("modstore: the stored files do not match their digest")
	}
	return wasm, manifest, nil
}

// ActiveLocal is every local module's active version with its files. A
// version whose files are gone or do not match their sums is marked failed
// and left out: the node never runs bytes it did not install.
func (s *Store) ActiveLocal(ctx context.Context) ([]Active, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT module, version, source, sha256, manifest_sha256, state, reason, installed_ms FROM module_versions
		WHERE source = ? AND state = 'active' ORDER BY module`, SourceLocal)
	if err != nil {
		return nil, fmt.Errorf("modstore: %w", err)
	}
	vs, err := scan(rows)
	if err != nil {
		return nil, fmt.Errorf("modstore: %w", err)
	}
	var out []Active
	for _, v := range vs {
		wasm, manifest, err := s.readLocal(v)
		if err != nil {
			s.log.Warn("modstore: the stored version fails its check; it does not run", "module", v.Module, "version", v.Version, "err", err)
			if err := s.setState(ctx, v.Module, v.SHA256, StateFailed, "at start: the stored files fail their sums"); err != nil {
				return nil, fmt.Errorf("modstore: %w", err)
			}
			continue
		}
		out = append(out, Active{Module: v.Module, SHA256: v.SHA256, Wasm: wasm, Manifest: manifest})
	}
	return out, nil
}

// rollbackLocal makes the previous version of a local module active again
// and retires the one it replaces.
func (s *Store) rollbackLocal(ctx context.Context, module string) (Outcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, err := s.versions(ctx, module, StateActive)
	if err != nil {
		return Outcome{}, fmt.Errorf("modstore: %w", err)
	}
	if len(cur) == 0 {
		return Outcome{}, fmt.Errorf("%w %q", ErrUnknownModule, module)
	}
	prevs, err := s.versions(ctx, module, StatePrevious)
	if err != nil {
		return Outcome{}, fmt.Errorf("modstore: %w", err)
	}
	if len(prevs) == 0 {
		s.count(ctx, module, ResultNothingToRollBack)
		return Outcome{Module: module, From: cur[0].Version, To: cur[0].Version, Result: ResultNothingToRollBack,
			Reason: "no previous version is kept"}, nil
	}
	prev := prevs[0]
	if _, _, err := s.readLocal(prev); err != nil {
		s.log.Warn("modstore: rollback refused", "module", module, "version", prev.Version, "err", err)
		s.count(ctx, module, ResultRefused)
		return Outcome{Module: module, From: cur[0].Version, To: prev.Version, Result: ResultRefused,
			Reason: "the previous version's files are missing or do not match their sums"}, nil
	}
	var retired []string
	err = sqlitedb.Immediate(ctx, s.db, func(c *sql.Conn) error {
		var err error
		if retired, err = retire(ctx, c, module, StateActive, prev.SHA256); err != nil {
			return err
		}
		_, err = c.ExecContext(ctx, `UPDATE module_versions SET state = 'active', reason = '' WHERE module = ? AND sha256 = ?`, module, prev.SHA256)
		return err
	})
	if err != nil {
		return Outcome{}, fmt.Errorf("modstore: %w", err)
	}
	s.removeFiles(module, retired)
	s.count(ctx, module, ResultRolledBack)
	return Outcome{Module: module, From: cur[0].Version, To: prev.Version, Result: ResultRolledBack}, nil
}
