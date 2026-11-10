package builder

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// CratesIO is the source cargo writes in Cargo.lock for a crate from
// crates.io.
const CratesIO = "registry+https://github.com/rust-lang/crates.io-index"

// Crate is one [[package]] of a Cargo.lock. The module's own package has no
// source and no checksum.
type Crate struct {
	Name     string
	Version  string
	Source   string
	Checksum string
}

var (
	// lockField is a [[package]] key with a plain quoted value.
	lockField = regexp.MustCompile(`^(name|version|source|checksum) = "([^"\\]*)"$`)
	// lockFormat is the lock's own version line, before the packages.
	lockFormat = regexp.MustCompile(`^version = [0-9]+$`)
	// lockDep is one line of a package's dependencies list.
	lockDep = regexp.MustCompile(`^ "[^"\\]+",$`)
	// tomlName is the package's name in Cargo.toml, a plain quoted value.
	tomlName = regexp.MustCompile(`^name\s*=\s*"([^"\\]+)"$`)
	// tomlNameKey is any line that sets the name or a key below it.
	tomlNameKey = regexp.MustCompile(`^name\s*[=.]`)
)

// ParseCargoLock reads the [[package]] blocks of a Cargo.lock as cargo
// writes it: comments and the format's version line before them, then each
// block's name, version, source and checksum lines and its dependencies
// list. Anything else (a [metadata] table, another key, a repeated key, an
// escaped or bare value) is an error: the node decides on the lock only when
// it reads every line of it (R-RUN-17, owner 2026-11-10).
func ParseCargoLock(b []byte) ([]Crate, error) {
	p := &lockParser{}
	for i, line := range strings.Split(string(b), "\n") {
		if err := p.line(line); err != nil {
			return nil, fmt.Errorf("builder: Cargo.lock line %d: %w", i+1, err)
		}
	}
	if err := p.end(); err != nil {
		return nil, fmt.Errorf("builder: Cargo.lock: %w", err)
	}
	return p.crates, nil
}

// lockParser is the state of ParseCargoLock: the crates so far, the keys of
// the last one and whether its dependencies list is open.
type lockParser struct {
	crates  []Crate
	seen    map[string]bool
	inDeps  bool
	version bool
}

func (p *lockParser) line(line string) error {
	switch {
	case p.inDeps:
		return p.dep(line)
	case line == "":
		return nil
	case line == "[[package]]":
		return p.open()
	case len(p.crates) == 0:
		return p.header(line)
	}
	return p.field(line)
}

// header is a line before the first package: a comment or the format line.
func (p *lockParser) header(line string) error {
	switch {
	case strings.HasPrefix(line, "#"):
		return nil
	case lockFormat.MatchString(line) && !p.version:
		p.version = true
		return nil
	}
	return errors.New("an unexpected line before the packages")
}

// open starts a package after checking the one before.
func (p *lockParser) open() error {
	if err := p.complete(); err != nil {
		return err
	}
	p.crates, p.seen = append(p.crates, Crate{}), map[string]bool{}
	return nil
}

// field is a line inside a package.
func (p *lockParser) field(line string) error {
	key, value := "dependencies", ""
	if line != "dependencies = [" {
		m := lockField.FindStringSubmatch(line)
		if m == nil {
			return errors.New("an unexpected line in a package")
		}
		key, value = m[1], m[2]
	}
	if p.seen[key] {
		return fmt.Errorf("%s twice in a package", key)
	}
	p.seen[key] = true
	c := &p.crates[len(p.crates)-1]
	switch key {
	case "name":
		c.Name = value
	case "version":
		c.Version = value
	case "source":
		c.Source = value
	case "checksum":
		c.Checksum = value
	default:
		p.inDeps = true
	}
	return nil
}

// dep is a line of an open dependencies list.
func (p *lockParser) dep(line string) error {
	switch {
	case line == "]":
		p.inDeps = false
		return nil
	case lockDep.MatchString(line):
		return nil
	}
	return errors.New("an unexpected line in a dependencies list")
}

// complete checks that the last package has a name and a version.
func (p *lockParser) complete() error {
	if len(p.crates) == 0 {
		return nil
	}
	if c := p.crates[len(p.crates)-1]; c.Name == "" || c.Version == "" {
		return errors.New("a package without a name or a version")
	}
	return nil
}

// end checks the lock once every line is read.
func (p *lockParser) end() error {
	switch {
	case p.inDeps:
		return errors.New("a dependencies list is not closed")
	case len(p.crates) == 0:
		return errors.New("no packages")
	}
	return p.complete()
}

// PackageName is the name in Cargo.toml's [package] table: one plain
// quoted name line. Any other form of it (a single-quoted string, an
// inherited name, two names, none) is an error.
func PackageName(cargoToml []byte) (string, error) {
	section, name, count := "", "", 0
	for line := range strings.SplitSeq(string(cargoToml), "\n") {
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "["):
			section = t
		case section != "[package]":
		case tomlName.MatchString(t):
			name, count = tomlName.FindStringSubmatch(t)[1], count+1
		case tomlNameKey.MatchString(t):
			return "", errors.New("builder: Cargo.toml: the package's name is not a plain quoted string")
		}
	}
	if count != 1 {
		return "", errors.New("builder: Cargo.toml: no single package name")
	}
	return name, nil
}
