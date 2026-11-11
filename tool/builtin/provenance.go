package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/igorrius/ganglion/builder"
)

// Origins of a module folder the node's own tools left (R-RUN-17).
const (
	originAgent   = "agent"    // module.write from a clean session
	originExample = "example:" // module.import, followed by the example's name
)

// Caps on what the provenance checks read.
const (
	maxProvenance = 256 << 10 // a record: 200 paths of 512 bytes with their escaping, and digests
	maxLock       = 1 << 20   // a Cargo.lock
)

// Files of a Rust module the dependency check reads.
const (
	cargoLock = "Cargo.lock"
	cargoToml = "Cargo.toml"
)

// sourceProvenance is the node's record of a module folder as its own tools
// left it: the digest of each file they wrote (builder.FileDigest), where
// the last write came from and when (ruling 7 of M4c, fix round 1: a digest
// per file, so a write of one file does not bless another's change).
type sourceProvenance struct {
	Files  map[string]string `json:"files"`
	Origin string            `json:"origin"` // "example:<name>" | "agent"
	At     time.Time         `json:"at"`
}

// provenancePath is the record of module name, a checked module name.
func (m Modules) provenancePath(name string) string {
	return filepath.Join(m.Provenance, name+".json")
}

// recordFolder records every file of the folder of module name as it is
// now: module.import's copy of an example.
func (m Modules) recordFolder(name, origin string) error {
	dir, err := m.moduleFolder(name)
	if err != nil {
		return err
	}
	files, err := builder.SourceDigests(dir)
	if err != nil {
		return err
	}
	return m.saveProvenance(name, sourceProvenance{Files: files, Origin: origin, At: m.Now().UTC()})
}

// recordFile records the one file module.write wrote, keeping the other
// entries; a new module's folder (fresh), or one whose record is missing or
// unreadable, starts with this file alone.
func (m Modules) recordFile(name, path string, content []byte, fresh bool) error {
	rec, err := m.readProvenance(name)
	if err != nil || fresh || rec.Files == nil {
		rec.Files = map[string]string{}
	}
	rec.Files[path] = builder.FileDigest(content)
	rec.Origin, rec.At = originAgent, m.Now().UTC()
	return m.saveProvenance(name, rec)
}

// forgetFile takes path out of the record of module name: a file written in
// a session that holds outside content is not the node's own, so ownSource is
// false until a clean session writes it again. A new folder (fresh) starts
// with an empty record, so nothing else in it is blessed either.
func (m Modules) forgetFile(name, path string, fresh bool) error {
	rec, err := m.readProvenance(name)
	if err != nil || fresh || rec.Files == nil {
		rec.Files = map[string]string{}
	}
	delete(rec.Files, path)
	rec.Origin, rec.At = originAgent, m.Now().UTC()
	return m.saveProvenance(name, rec)
}

// saveProvenance writes the record of module name atomically.
func (m Modules) saveProvenance(name string, rec sourceProvenance) error {
	if m.Provenance == "" {
		return errors.New("the node has no provenance folder")
	}
	raw, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.Provenance, 0o700); err != nil {
		return err
	}
	return writeFile(m.provenancePath(name), raw)
}

// ownSource reports whether the folder of module name is the agent's own
// work: every file in it has a recorded digest equal to its own and no
// recorded file is missing, and its dependencies are the examples' crates
// (vettedCrates; owner, 2026-11-10). A missing or unreadable record, or a
// folder that cannot be digested, is not (fail closed): the answers about it
// stay outside content (R-RUN-13, R-RUN-17).
func (m Modules) ownSource(name string) bool {
	if m.Provenance == "" {
		return false
	}
	rec, err := m.readProvenance(name)
	if err != nil {
		return false
	}
	dir, err := m.moduleFolder(name)
	if err != nil {
		return false
	}
	now, err := builder.SourceDigests(dir)
	if err != nil || !maps.Equal(now, rec.Files) {
		return false
	}
	return m.vettedCrates(dir, rec.Files)
}

// readProvenance reads the record of module name.
func (m Modules) readProvenance(name string) (sourceProvenance, error) {
	f, err := os.Open(m.provenancePath(name))
	if err != nil {
		return sourceProvenance{}, err
	}
	defer func() { _ = f.Close() }()
	raw, err := readCapped(f, maxProvenance)
	if err != nil {
		return sourceProvenance{}, err
	}
	var rec sourceProvenance
	if err := strictJSON(raw, &rec); err != nil {
		return sourceProvenance{}, err
	}
	return rec, nil
}

// vettedCrates reports whether the module in dir builds only with crates the
// node's examples lock: its Cargo.lock (as recorded) parses, holds one root
// package without a source, named as its Cargo.toml (as recorded) names the
// package, and every other package is from crates.io with a checksum and
// is, name, version and checksum alike, in an example's Cargo.lock. A
// dependency's build script or macro could otherwise write any text into
// the build's answer, or rewrite the source before it is compiled (owner,
// 2026-11-10).
func (m Modules) vettedCrates(dir string, files map[string]string) bool {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	lock, err := readRecorded(root, cargoLock, files)
	if err != nil {
		return false
	}
	toml, err := readRecorded(root, cargoToml, files)
	if err != nil {
		return false
	}
	crates, err := builder.ParseCargoLock(lock)
	if err != nil {
		return false
	}
	pkg, err := builder.PackageName(toml)
	if err != nil {
		return false
	}
	return onlyVetted(crates, pkg, m.examplesCrates())
}

// onlyVetted reports whether crates are one root package named pkg and
// crates of vetted.
func onlyVetted(crates []builder.Crate, pkg string, vetted map[builder.Crate]bool) bool {
	roots := 0
	for _, c := range crates {
		switch {
		case c.Source == "" && c.Checksum == "" && c.Name == pkg:
			roots++
		case !registryCrate(c) || !vetted[c]:
			return false
		}
	}
	return roots == 1
}

// registryCrate reports whether c is from crates.io with a checksum.
func registryCrate(c builder.Crate) bool {
	return c.Source == builder.CratesIO && digestPattern.MatchString(c.Checksum)
}

// examplesCrates are the crates.io crates the examples' Cargo.lock files
// list (Examples/*/Cargo.lock); none on a node without examples. An example
// whose lock does not read adds none.
func (m Modules) examplesCrates() map[builder.Crate]bool {
	out := map[builder.Crate]bool{}
	if m.Examples == "" {
		return out
	}
	entries, err := os.ReadDir(m.Examples)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		for _, c := range exampleLock(filepath.Join(m.Examples, e.Name(), cargoLock)) {
			if registryCrate(c) {
				out[c] = true
			}
		}
	}
	return out
}

// exampleLock is the crates of the lock at path, a regular file; none when
// it is missing or does not parse.
func exampleLock(path string) []builder.Crate {
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	raw, err := readCapped(f, maxLock)
	if err != nil {
		return nil
	}
	crates, err := builder.ParseCargoLock(raw)
	if err != nil {
		return nil
	}
	return crates
}

// readRecorded reads the file rel of a module's root, at most maxLock bytes,
// and checks that it is the content the record holds for it.
func readRecorded(root *os.Root, rel string, files map[string]string) ([]byte, error) {
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := readCapped(f, maxLock)
	if err != nil {
		return nil, err
	}
	if want, ok := files[rel]; !ok || builder.FileDigest(raw) != want {
		return nil, fmt.Errorf("%s is not as recorded", rel)
	}
	return raw, nil
}

// readCapped reads r whole, refusing more than limit bytes.
func readCapped(r io.Reader, limit int) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, fmt.Errorf("over %d bytes", limit)
	}
	return raw, nil
}

// moduleLocks serialises the writes to each module's folder: module.write
// and module.import take the lock of the module's name, so no write undoes
// another (fix round 1 of M4c task 6).
type moduleLocks struct {
	mu     sync.Mutex
	byName map[string]*sync.Mutex
}

func newModuleLocks() *moduleLocks { return &moduleLocks{byName: map[string]*sync.Mutex{}} }

// lock takes the lock of module name and returns its release.
func (l *moduleLocks) lock(name string) func() {
	l.mu.Lock()
	mu, ok := l.byName[name]
	if !ok {
		mu = &sync.Mutex{}
		l.byName[name] = mu
	}
	l.mu.Unlock()
	mu.Lock()
	return mu.Unlock
}
