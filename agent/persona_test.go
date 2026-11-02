package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPersonaDir(t *testing.T) {
	ws := t.TempDir()
	read := PersonaDir(ws)
	if got, err := read(); err != nil || got != "" {
		t.Fatalf("no persona dir: %q, %v", got, err)
	}
	dir := filepath.Join(ws, "persona")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"20-style.md":    "Answer in Ukrainian.\n",
		"10-identity.md": "# Identity\n\nYou are Jarvis.\n",
		"notes.txt":      "ignored",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := read()
	if err != nil || got != "# Identity\n\nYou are Jarvis.\n\nAnswer in Ukrainian." {
		t.Fatalf("persona %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "30-big.md"), []byte(strings.Repeat("x", MaxPersonaBytes)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := read(); err == nil || !strings.Contains(err.Error(), "persona") {
		t.Fatalf("an oversized persona: %v", err)
	}
}
