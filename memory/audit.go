package memory

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/guard"
)

// dirPersona holds the persona files (agent.PersonaDir).
const dirPersona = "persona"

// Finding is one flagged file of an audit.
type Finding struct {
	Path   string // in the repository, e.g. memory/car.md
	Reason string
}

// Report is the outcome of an audit.
type Report struct {
	Facts, Persona int // how many were checked
	Findings       []Finding
}

// String is the report for the owner.
func (r Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Checked %d stored facts and %d persona files.", r.Facts, r.Persona)
	if len(r.Findings) == 0 {
		b.WriteString(" Nothing was flagged.")
		return b.String()
	}
	b.WriteString(" Flagged (nothing was changed or deleted):")
	for _, f := range r.Findings {
		fmt.Fprintf(&b, "\n- %s: %s", f.Path, f.Reason)
	}
	return b.String()
}

// Audit runs the clean-context check over every stored fact and every
// persona file and reports what it flags (R-POL-7, the scheduled check).
// It changes nothing: the owner decides. An error is the context's or
// an unreadable directory.
func (m *Memory) Audit(ctx context.Context) (Report, error) {
	es, err := m.list(CheckOK)
	if err != nil {
		return Report{}, err
	}
	var r Report
	for _, e := range es {
		v, err := m.guard.CheckFact(ctx, e.Title+"\n\n"+e.Body)
		if err != nil {
			return Report{}, fmt.Errorf("memory: audit %s: %w", e.Slug, err)
		}
		r.Facts++
		r.flag(pathOf(e.Slug, CheckOK), v)
	}
	if err := m.auditPersona(ctx, &r); err != nil {
		return Report{}, err
	}
	return r, nil
}

// flag adds a finding when v is not OK.
func (r *Report) flag(path string, v guard.Verdict) {
	if !v.OK {
		r.Findings = append(r.Findings, Finding{Path: path, Reason: v.Reason})
	}
}

// auditPersona checks each persona/*.md file as it is on disk.
func (m *Memory) auditPersona(ctx context.Context, r *Report) error {
	dir, err := m.repo.Path(dirPersona)
	if err != nil {
		return err
	}
	files, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("memory: audit persona: %w", err)
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".md") {
			names = append(names, f.Name())
		}
	}
	slices.Sort(names)
	for _, name := range names {
		rel := dirPersona + "/" + name
		full, err := m.repo.Path(rel)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(full)
		if err != nil {
			return fmt.Errorf("memory: audit persona: %w", err)
		}
		v, err := m.guard.CheckPersona(ctx, string(data))
		if err != nil {
			return fmt.Errorf("memory: audit %s: %w", rel, err)
		}
		r.Persona++
		r.flag(rel, v)
	}
	return nil
}
