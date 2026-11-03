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

// IdentityFile is the persona file the first-run introduction writes.
const IdentityFile = dirPersona + "/10-identity.md"

// ErrPersonaExists is a persona written while persona/ already has a
// file: the introduction happens once, and after it the persona changes
// only by hand (design section 5.8).
var ErrPersonaExists = errors.New("memory: persona/ already has a file")

// PersonaEmpty reports whether persona/ has no *.md file, so the agent
// has not been introduced yet.
func (m *Memory) PersonaEmpty() (bool, error) {
	names, err := m.personaFiles()
	return len(names) == 0, err
}

// CheckPersona asks the clean-context check whether text is an ordinary
// description of the assistant (R-POL-7), before anyone is asked to
// approve it.
func (m *Memory) CheckPersona(ctx context.Context, text string) (guard.Verdict, error) {
	return m.guard.CheckPersona(ctx, text)
}

// WriteIdentity stores text as persona/10-identity.md in one commit that
// names the turn, provided persona/ still has no *.md file at this
// moment; otherwise it is ErrPersonaExists, so of two approvals made at
// once only the first writes. Writing the very text that is already the
// only persona file again (a rerun of the approved call) is not an
// error. The caller has checked the text.
func (m *Memory) WriteIdentity(ctx context.Context, text, turn string) error {
	data := []byte(strings.TrimSpace(text) + "\n")
	m.mu.Lock()
	defer m.mu.Unlock()
	names, err := m.personaFiles()
	if err != nil {
		return err
	}
	if len(names) > 0 {
		same, err := m.isIdentity(names, data)
		if err != nil {
			return err
		}
		if !same {
			return ErrPersonaExists
		}
	}
	if err := m.writeFile(IdentityFile, data); err != nil {
		return err
	}
	if _, err := m.repo.Commit(ctx, []string{IdentityFile}, fmt.Sprintf("persona: introduction (turn %s)", turn)); err != nil {
		return fmt.Errorf("memory: commit the persona: %w", err)
	}
	return nil
}

// isIdentity reports whether names is just the identity file and holds
// data.
func (m *Memory) isIdentity(names []string, data []byte) (bool, error) {
	if !slices.Equal(names, []string{"10-identity.md"}) {
		return false, nil
	}
	full, err := m.repo.Path(IdentityFile)
	if err != nil {
		return false, err
	}
	have, err := os.ReadFile(full)
	if err != nil {
		return false, fmt.Errorf("memory: %w", err)
	}
	return slices.Equal(have, data), nil
}

// personaFiles lists the *.md files of persona/ in name order; a missing
// directory has none.
func (m *Memory) personaFiles() ([]string, error) {
	dir, err := m.repo.Path(dirPersona)
	if err != nil {
		return nil, err
	}
	files, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("memory: persona files: %w", err)
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		if !f.IsDir() && strings.HasSuffix(f.Name(), ".md") {
			names = append(names, f.Name())
		}
	}
	slices.Sort(names)
	return names, nil
}
