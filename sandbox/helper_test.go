//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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
	grants, err := Grants(deny, os.ReadDir, false) // the default: split folders are listed
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
	grants, err := Grants([]string{filepath.Join(inner, "denied")}, os.ReadDir, false) // splits inner
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
	// A password prompt would refuse too; the flag is what must refuse it.
	if !strings.Contains(out, "no new privileges") && !strings.Contains(out, "effective uid is not 0") {
		t.Fatalf("sudo refused for another reason than no_new_privs: %q", out)
	}
}

func TestAMissingProgramFailsBeforeRunning(t *testing.T) {
	if _, code := run(t, nil, "no-such-program-ganglion"); code != exitSetup {
		t.Fatalf("exit %d, want %d", code, exitSetup)
	}
}

func TestNewIsReadyWhereTheKernelHoldsIt(t *testing.T) {
	abi, err := landlockABI()
	if err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := New(self, Policy{Env: []string{"PATH=/usr/bin:/bin"}}, fakeKernel{euid: 1000, abi: abi}, time.Unix(0, 0))
	if ok, r := s.Ready(); !ok {
		t.Fatalf("not ready: %v", r)
	}
	dir, file := secretDir(t)
	s = New(self, Policy{Deny: func() []string { return []string{dir} }, Env: []string{"PATH=/usr/bin:/bin"}}, fakeKernel{euid: 1000, abi: abi}, time.Unix(0, 0))
	cmd, err := s.Command(t.Context(), []string{"cat", file}, "/")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := cmd.CombinedOutput(); err == nil || strings.Contains(string(out), "hunter2") {
		t.Fatalf("read a denied file through Command: %v, %q", err, out)
	}
}

// ready is a sandbox on this kernel that starts the helper from the
// process's own image, as the node does; it skips without Landlock.
func ready(t *testing.T, p Policy) *Sandbox {
	t.Helper()
	abi, err := landlockABI()
	if err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	if p.Env == nil {
		p.Env = []string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8"}
	}
	s := New("/proc/self/exe", p, fakeKernel{euid: 1000, abi: abi}, time.Unix(0, 0))
	if ok, r := s.Ready(); !ok {
		t.Fatalf("not ready: %v", r)
	}
	return s
}

// output runs argv in dir through s and returns what it printed and its
// exit code; a command that never ran fails the test.
func output(t *testing.T, s *Sandbox, dir string, argv ...string) (string, int) {
	t.Helper()
	cmd, err := s.Command(t.Context(), argv, dir)
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	Release(cmd)
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(out), 0
	case errors.As(err, &exit) && exit.ExitCode() != exitSetup:
		return string(out), exit.ExitCode()
	}
	t.Fatalf("the command never ran: %v, %q", err, out)
	return "", 0
}

// TestAPathCreatedAfterStartIsDenied: a secret store that appears after the
// node started is denied too (a review's escape: ~/.aws made later).
func TestAPathCreatedAfterStartIsDenied(t *testing.T) {
	dir, _ := secretDir(t)
	later := filepath.Join(dir, "later")
	s := ready(t, Policy{Deny: func() []string { return DenyList("", "/", NodeFiles{}, nil, []string{later}) }})
	if err := os.MkdirAll(later, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(later, "credentials"), []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, code := output(t, s, "/", "cat", filepath.Join(later, "credentials")); code == 0 || strings.Contains(out, "hunter2") {
		t.Fatalf("read a secret created after start: exit %d, %q", code, out)
	}
}

// TestAnEnvSymlinkIsDenied: a .env that points elsewhere denies its target.
func TestAnEnvSymlinkIsDenied(t *testing.T) {
	dir, file := secretDir(t)
	work := filepath.Join(dir, "work")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(file, filepath.Join(work, ".env")); err != nil {
		t.Fatal(err)
	}
	s := ready(t, Policy{EnvDepth: 2})
	if out, code := output(t, s, work, "cat", ".env"); code == 0 || strings.Contains(out, "hunter2") {
		t.Fatalf("read a .env through its symlink: exit %d, %q", code, out)
	}
}

// TestARelativeProgramRunsInItsFolder: a skill's ./script is found in the
// command's folder, not the helper's.
func TestARelativeProgramRunsInItsFolder(t *testing.T) {
	dir, _ := secretDir(t)
	if err := os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho ran\n"), 0o700); err != nil { //nolint:gosec // a script the test runs
		t.Fatal(err)
	}
	s := ready(t, Policy{})
	if out, code := output(t, s, dir, "./run.sh"); code != 0 || !strings.Contains(out, "ran") {
		t.Fatalf("./run.sh: exit %d, %q", code, out)
	}
}

// TestAbstractSocketsAreScoped: a listener outside the sandbox on an
// abstract Unix socket cannot be reached from inside (Landlock ABI 6).
func TestAbstractSocketsAreScoped(t *testing.T) {
	if abi, err := landlockABI(); err != nil || abi < 6 {
		t.Skip("no Landlock scopes here")
	}
	name := "@ganglion-sandbox-test-" + strconv.Itoa(os.Getpid())
	ln, err := net.Listen("unix", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("SOCKSECRET"))
			_ = c.Close()
		}
	}()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := ready(t, Policy{Env: []string{"PATH=/usr/bin:/bin", connectEnv + "=" + name}})
	out, _ := output(t, s, "/", self, "-test.run=^TestConnectFromInside$")
	if strings.Contains(out, "SOCKSECRET") {
		t.Fatalf("reached an abstract socket outside the sandbox: %q", out)
	}
	if !strings.Contains(out, "refused:") {
		t.Fatalf("the probe inside did not run: %q", out)
	}
}

// connectEnv names the socket TestConnectFromInside dials; it is set only
// inside the sandbox, by TestAbstractSocketsAreScoped.
const connectEnv = "GANGLION_SANDBOX_TEST_CONNECT"

// TestConnectFromInside is the probe TestAbstractSocketsAreScoped runs in
// the sandbox; on its own it skips.
func TestConnectFromInside(t *testing.T) {
	name := os.Getenv(connectEnv)
	if name == "" {
		t.Skip("runs only inside the sandbox")
	}
	c, err := net.Dial("unix", name)
	if err != nil {
		fmt.Println("refused:", err)
		return
	}
	b, _ := io.ReadAll(c)
	fmt.Println("got:", string(b))
}

// TestPathSocketsAreRefused: below Landlock 9 a command opens no Unix
// socket, so a listener in an allowed folder (gpg-agent, the keyring's
// D-Bus) is out of reach (owner, 2026-11-08).
func TestPathSocketsAreRefused(t *testing.T) {
	dir, _ := secretDir(t)
	name := filepath.Join(dir, "agent.sock")
	ln, err := net.Listen("unix", name)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_, _ = c.Write([]byte("SOCKSECRET"))
			_ = c.Close()
		}
	}()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	s := ready(t, Policy{Env: []string{"PATH=/usr/bin:/bin", connectEnv + "=" + name}})
	out, _ := output(t, s, "/", self, "-test.run=^TestConnectFromInside$")
	if strings.Contains(out, "SOCKSECRET") || !strings.Contains(out, "refused:") {
		t.Fatalf("a Unix socket outside the sandbox: %q", out)
	}
}

// TestEverydayCommandsStillRun: refusing Unix sockets leaves the read
// commands working (ls -l asks NSS for names, which falls back to files).
func TestEverydayCommandsStillRun(t *testing.T) {
	s := ready(t, Policy{})
	for _, argv := range [][]string{{"ls", "-l", "/"}, {"ps", "-e"}, {"df", "-h", "/"}} {
		if _, err := exec.LookPath(argv[0]); err != nil {
			continue
		}
		if out, code := output(t, s, "/", argv...); code != 0 {
			t.Errorf("%v: exit %d, %q", argv, code, out)
		}
	}
}

// splitTree is top/secret/key, denied with its folder, beside
// top/open/file, which is not; it returns top.
func splitTree(t *testing.T) string {
	t.Helper()
	dir, _ := secretDir(t)
	top := filepath.Join(dir, "top")
	for _, f := range []string{"secret/key", "open/file"} {
		p := filepath.Join(top, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("hunter2"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return top
}

// ADR-0057, abuse test (conventions, section 11): a folder split around a
// denied one may be listed, and so may the denied folder through it, but
// nothing in it is read and nothing is written beside it.
func TestASplitFolderIsListedNotRead(t *testing.T) {
	top := splitTree(t)
	secret := filepath.Join(top, "secret")
	s := ready(t, Policy{Deny: func() []string { return []string{secret} }})
	if out, code := output(t, s, "/", "ls", top); code != 0 || !strings.Contains(out, "open") || !strings.Contains(out, "secret") {
		t.Errorf("ls top: exit %d, %q", code, out)
	}
	if out, code := output(t, s, "/", "ls", secret); code != 0 || !strings.Contains(out, "key") {
		t.Errorf("ls top/secret: exit %d, %q", code, out)
	}
	if out, code := output(t, s, "/", "cat", filepath.Join(top, "open", "file")); code != 0 || !strings.Contains(out, "hunter2") {
		t.Errorf("cat top/open/file: exit %d, %q", code, out)
	}
	if out, code := output(t, s, "/", "cat", filepath.Join(secret, "key")); code == 0 || strings.Contains(out, "hunter2") ||
		!strings.Contains(out, "Permission denied") {
		t.Errorf("read a denied file in a listed folder: exit %d, %q", code, out)
	}
	for _, argv := range [][]string{{"touch", filepath.Join(top, "new")}, {"mkdir", filepath.Join(top, "newdir")},
		{"touch", filepath.Join(secret, "new")}, {"rm", filepath.Join(secret, "key")}} {
		if out, code := output(t, s, "/", argv...); code == 0 {
			t.Errorf("%v: a list grant wrote: %q", argv, out)
		}
	}
	if _, err := os.Stat(filepath.Join(secret, "key")); err != nil {
		t.Errorf("the denied file is gone: %v", err)
	}
}

// sandbox.hide_names: true: a split folder is not listed, as before
// ADR-0057.
func TestHideNamesKeepsASplitFolderUnlisted(t *testing.T) {
	top := splitTree(t)
	secret := filepath.Join(top, "secret")
	s := ready(t, Policy{Deny: func() []string { return []string{secret} }, HideNames: true})
	for _, dir := range []string{top, secret} {
		if out, code := output(t, s, "/", "ls", dir); code == 0 || strings.Contains(out, "key") {
			t.Errorf("ls %s with hidden names: exit %d, %q", dir, code, out)
		}
	}
	if out, code := output(t, s, "/", "cat", filepath.Join(top, "open", "file")); code != 0 {
		t.Errorf("cat top/open/file with hidden names: exit %d, %q", code, out)
	}
}
