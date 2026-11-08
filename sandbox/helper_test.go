//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// run runs argv in the sandbox with deny denied, and returns its output and
// exit code. It skips where the kernel has no Landlock.
func run(t *testing.T, deny []string, argv ...string) (string, int) {
	t.Helper()
	abi, err := landlockABI()
	if err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	grants, err := Grants(deny, os.ReadDir)
	if err != nil {
		t.Fatal(err)
	}
	sp := spec{Argv: argv, Env: []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}, Dir: "/", Grants: grants, ABI: abi}
	cmd, err := command(context.Background(), self, sp)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	err = cmd.Run()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return out.String(), 0
	case errors.As(err, &exit):
		return out.String(), exit.ExitCode()
	}
	t.Fatal(err)
	return "", 0
}

func secretDir(t *testing.T) (dir, file string) {
	t.Helper()
	dir = t.TempDir()
	file = filepath.Join(dir, "ganglion.db")
	if err := os.WriteFile(file, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved, filepath.Join(resolved, "ganglion.db")
}

func TestADeniedFileCannotBeRead(t *testing.T) {
	dir, file := secretDir(t)
	out, code := run(t, []string{dir}, "cat", file)
	if code == 0 || code == exitSetup || strings.Contains(out, "hunter2") {
		t.Fatalf("read a denied file: exit %d, %q", code, out)
	}
}

func TestADeniedFolderCannotBeWritten(t *testing.T) {
	dir, _ := secretDir(t)
	if _, code := run(t, []string{dir}, "touch", filepath.Join(dir, "new")); code == 0 || code == exitSetup {
		t.Fatal("created a file in a denied folder")
	}
}

func TestASymlinkDoesNotReachADeniedFile(t *testing.T) {
	dir, file := secretDir(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if out, code := run(t, []string{dir}, "cat", link); code == 0 || code == exitSetup || strings.Contains(out, "hunter2") {
		t.Fatalf("read a denied file through a symlink: exit %d, %q", code, out)
	}
}

func TestAnAllowedFileReads(t *testing.T) {
	if _, err := os.Stat("/etc/hostname"); err != nil {
		t.Skip("no /etc/hostname")
	}
	dir, _ := secretDir(t)
	out, code := run(t, []string{dir}, "cat", "/etc/hostname")
	if code != 0 {
		t.Fatalf("an allowed file did not read: exit %d, %q", code, out)
	}
}

// TestAFileCreatedAfterTheGrantsIsDenied pins fail closed: a split folder's
// grants are its entries at the time, so a newcomer is reached by nothing.
func TestAFileCreatedAfterTheGrantsIsDenied(t *testing.T) {
	abi, err := landlockABI()
	if err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	dir, _ := secretDir(t)
	inner := filepath.Join(dir, "inner")
	if err := os.Mkdir(inner, 0o700); err != nil {
		t.Fatal(err)
	}
	grants, err := Grants([]string{filepath.Join(inner, "denied")}, os.ReadDir) // splits inner
	if err != nil {
		t.Fatal(err)
	}
	late := filepath.Join(inner, "late")
	if err := os.WriteFile(late, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd, err := command(t.Context(), self, spec{Argv: []string{"cat", late}, Env: []string{"PATH=/usr/bin:/bin"},
		Dir: "/", Grants: grants, ABI: abi})
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() == exitSetup || strings.Contains(string(out), "hunter2") {
		t.Fatalf("a file created after the grants was read, or cat never ran: %v, %q", err, out)
	}
}

func TestNoNewPrivsIsSet(t *testing.T) {
	out, code := run(t, nil, "grep", "NoNewPrivs", "/proc/self/status")
	if code != 0 || !strings.Contains(out, "1") {
		t.Fatalf("no_new_privs is not set: exit %d, %q", code, out)
	}
}

func TestTheEnvironmentIsTheAllowlist(t *testing.T) {
	t.Setenv("TELEGRAM_BOT_TOKEN", "hunter2")
	out, code := run(t, nil, "env")
	if code != 0 || strings.Contains(out, "hunter2") || strings.Contains(out, "TELEGRAM") {
		t.Fatalf("the node's environment leaked: exit %d, %q", code, out)
	}
	if !strings.Contains(out, "LANG=C.UTF-8") {
		t.Fatalf("the allowlist is missing: %q", out)
	}
}

func TestSudoGainsNothing(t *testing.T) {
	if _, err := exec.LookPath("sudo"); err != nil {
		t.Skip("no sudo here")
	}
	out, code := run(t, nil, "sudo", "-n", "true")
	if code == 0 || code == exitSetup {
		t.Fatalf("sudo ran: %q", out)
	}
}

func TestAMissingProgramFailsBeforeRunning(t *testing.T) {
	if _, code := run(t, nil, "no-such-program-ganglion"); code != exitSetup {
		t.Fatalf("exit %d, want %d", code, exitSetup)
	}
}
