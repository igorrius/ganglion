package config

import (
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestLabStaysOutOfTheBinary holds chatlab's boundary (ADR-0048): no fake, no
// clock and no override reaches the ganglion binary.
func TestLabStaysOutOfTheBinary(t *testing.T) {
	output := func(name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = ".."
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %v: %v\n%s", name, args, err, out)
		}
		return string(out)
	}

	const module = "github.com/igorrius/ganglion/"
	deps := strings.Fields(output("go", "list", "-deps", "."))
	for _, banned := range []string{
		module + "transport/telegram/telegramtest",
		module + "provider/providertest",
		module + "internal/chatlab",
		module + "tools/chatlab",
		"testing",
	} {
		if slices.Contains(deps, banned) {
			t.Errorf("the binary depends on %s", banned)
		}
	}

	files := strings.Fields(strings.Trim(strings.TrimSpace(output("go", "list", "-f", "{{.GoFiles}}", "./config")), "[]"))
	if slices.Contains(files, "lab.go") {
		t.Error("a default build of config holds lab.go")
	}

	bin := filepath.Join(t.TempDir(), "ganglion")
	output("go", "build", "-o", bin, ".")
	if nm := output("go", "tool", "nm", bin); strings.Contains(nm, "ApplyLab") {
		t.Error("the binary holds ApplyLab")
	}
}
