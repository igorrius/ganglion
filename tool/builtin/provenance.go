package builtin

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/builder"
)

// Origins of a module folder the node's own tools left (R-RUN-17).
const (
	originAgent   = "agent"    // module.write from a clean session
	originExample = "example:" // module.import, followed by the example's name
)

// maxProvenance caps the bytes of a provenance record read back.
const maxProvenance = 4 << 10

// sourceProvenance is the node's record of a module folder as its own tools last
// left it: the folder's digest (builder.SourceDigest), where it came from
// and when (ruling 7 of M4c).
type sourceProvenance struct {
	Digest string    `json:"digest"`
	Origin string    `json:"origin"` // "example:<name>" | "agent"
	At     time.Time `json:"at"`
}

// provenancePath is the record of module name, a checked module name.
func (m Modules) provenancePath(name string) string {
	return filepath.Join(m.Provenance, name+".json")
}

// recordProvenance digests the folder of module name now and writes the
// record atomically, replacing the one before.
func (m Modules) recordProvenance(name, origin string) error {
	if m.Provenance == "" {
		return errors.New("the node has no provenance folder")
	}
	dir, err := m.moduleFolder(name)
	if err != nil {
		return err
	}
	digest, err := builder.SourceDigest(dir)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(sourceProvenance{Digest: digest, Origin: origin, At: m.Now().UTC()})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(m.Provenance, 0o700); err != nil {
		return err
	}
	return writeFile(m.provenancePath(name), raw)
}

// ownSource reports whether the folder of module name is as the node's own
// tools last left it: its digest now equals the record. A missing or
// unreadable record, or a folder that cannot be digested, is not (fail
// closed): the answers about it stay outside content (R-RUN-13).
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
	digest, err := builder.SourceDigest(dir)
	return err == nil && digestPattern.MatchString(rec.Digest) && digest == rec.Digest
}

// readProvenance reads the record of module name.
func (m Modules) readProvenance(name string) (sourceProvenance, error) {
	f, err := os.Open(m.provenancePath(name))
	if err != nil {
		return sourceProvenance{}, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, maxProvenance+1))
	if err != nil {
		return sourceProvenance{}, err
	}
	if len(raw) > maxProvenance {
		return sourceProvenance{}, fmt.Errorf("the record of %s is over %d bytes", name, maxProvenance)
	}
	var rec sourceProvenance
	if err := strictJSON(raw, &rec); err != nil {
		return sourceProvenance{}, err
	}
	return rec, nil
}
