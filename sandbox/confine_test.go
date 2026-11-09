//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// confined runs argv through s.Confined writing only beneath write, and
// returns what it printed and its exit code; a command that never ran fails
// the test.
func confined(t *testing.T, s *Sandbox, write []string, argv ...string) (string, int) {
	t.Helper()
	cmd, err := s.Confined(t.Context(), write, argv, "/")
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

// resolvedTemp is a fresh temporary folder by its real path.
func resolvedTemp(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// ADR-0064, abuse test: a confined command creates, renames and removes
// beneath its folder, reads everywhere it could before, and writes nothing
// elsewhere: not another temporary folder, not home, not a file it can read.
func TestAConfinedCommandWritesOnlyItsFolder(t *testing.T) {
	s := ready(t, Policy{})
	work, other := resolvedTemp(t), resolvedTemp(t)
	existing := filepath.Join(other, "existing")
	if err := os.WriteFile(existing, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	inHome := filepath.Join(home, fmt.Sprintf(".ganglion-confine-test-%d", os.Getpid()))
	t.Cleanup(func() { _ = os.Remove(inHome) })
	inside := `echo ok > "$1/new" && mkdir "$1/d" && mv "$1/new" "$1/d/new" && ln -s new "$1/d/link" && rm -r "$1/d" && echo x > /dev/null`
	if out, code := confined(t, s, []string{work}, "sh", "-c", inside, "sh", work); code != 0 {
		t.Fatalf("writing its own folder: exit %d, %q", code, out)
	}
	if out, code := confined(t, s, []string{work}, "cat", existing); code != 0 || out != "original" {
		t.Fatalf("reading outside its folder: exit %d, %q", code, out)
	}
	for _, argv := range [][]string{
		{"touch", filepath.Join(other, "new")}, {"mkdir", filepath.Join(other, "dir")},
		{"sh", "-c", `echo x >> "$1"`, "sh", existing}, {"truncate", "-s", "0", existing}, {"rm", existing},
		{"mv", existing, filepath.Join(work, "taken")}, {"ln", "-s", "/", filepath.Join(other, "link")},
		{"mkfifo", filepath.Join(other, "fifo")}, {"touch", inHome},
	} {
		if out, code := confined(t, s, []string{work}, argv...); code == 0 {
			t.Errorf("%q wrote outside its folder: %q", argv, out)
		}
	}
	if b, err := os.ReadFile(existing); err != nil || string(b) != "original" {
		t.Errorf("the file outside was changed: %q, %v", b, err)
	}
	for _, p := range []string{filepath.Join(other, "new"), filepath.Join(other, "dir"), filepath.Join(other, "link"),
		filepath.Join(other, "fifo"), inHome} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists after a confined command: %v", p, err)
		}
	}
}

// The deny list holds for a confined command too, inside its own folder
// as well, and a denied folder cannot be made writable.
func TestAConfinedCommandMeetsTheDenyList(t *testing.T) {
	dir, file := secretDir(t)
	work := resolvedTemp(t)
	key := filepath.Join(work, "secret", "key")
	if err := os.MkdirAll(filepath.Dir(key), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(key, []byte("hunter2"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := ready(t, Policy{Deny: func() []string { return []string{dir, filepath.Dir(key)} }})
	for _, argv := range [][]string{{"cat", file}, {"cat", key}, {"sh", "-c", `echo x > "$1"`, "sh", key}, {"rm", key}} {
		if out, code := confined(t, s, []string{work}, argv...); code == 0 || strings.Contains(out, "hunter2") {
			t.Errorf("%q reached a denied path: %q", argv, out)
		}
	}
	// work is split around the denied folder: what it held is written, a new
	// entry directly in it is not (fail closed, as for every split folder).
	open := filepath.Join(work, "open")
	if err := os.Mkdir(open, 0o700); err != nil {
		t.Fatal(err)
	}
	if out, code := confined(t, s, []string{work}, "touch", filepath.Join(open, "new")); code != 0 {
		t.Errorf("writing beside the denied folder: exit %d, %q", code, out)
	}
	if _, err := s.Confined(t.Context(), []string{dir}, []string{"true"}, "/"); err == nil {
		t.Error("a denied folder was made writable")
	}
}

// A kernel whose Landlock cannot govern truncation runs no confined
// command (fail closed), while host.exec's commands still run.
func TestConfinedNeedsLandlock3(t *testing.T) {
	if _, err := landlockABI(); err != nil {
		t.Skipf("no Landlock here: %v", err)
	}
	s := New("/proc/self/exe", Policy{Env: []string{"PATH=/usr/bin:/bin"}}, fakeKernel{euid: 1000, abi: 2}, time.Unix(0, 0))
	if ok, r := s.Ready(); !ok {
		t.Fatalf("not ready: %v", r)
	}
	if _, err := s.Confined(t.Context(), []string{t.TempDir()}, []string{"true"}, "/"); !errors.Is(err, ErrCannotConfine) {
		t.Fatalf("Confined on ABI 2: %v, want ErrCannotConfine", err)
	}
	if _, err := s.Command(t.Context(), []string{"true"}, "/"); err != nil {
		t.Fatalf("Command on ABI 2: %v", err)
	}
}

// sleeping are the pids of the live `sleep <secs>` processes.
func sleeping(secs string) []int {
	want := "sleep\x00" + secs + "\x00"
	entries, _ := os.ReadDir("/proc")
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		if b, err := os.ReadFile("/proc/" + e.Name() + "/cmdline"); err == nil && strings.HasSuffix(string(b), want) {
			out = append(out, pid)
		}
	}
	return out
}

// uniqueSleep is a sleep length no other test uses, with its cleanup.
func uniqueSleep(t *testing.T) string {
	t.Helper()
	secs := fmt.Sprintf("7%d.%d", os.Getpid()%10, time.Now().UnixNano()%1_000_000)
	t.Cleanup(func() {
		for _, pid := range sleeping(secs) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	return secs
}

// ADR-0064, abuse test: what a confined command detaches with setsid, out
// of its process group, ends with it.
func TestAConfinedCommandLeavesNoProcess(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid here")
	}
	s := ready(t, Policy{})
	secs := uniqueSleep(t)
	script := `setsid sleep "$1" </dev/null >/dev/null 2>&1 & echo started`
	if out, code := confined(t, s, []string{resolvedTemp(t)}, "sh", "-c", script, "sh", secs); code != 0 {
		t.Fatalf("exit %d, %q", code, out)
	}
	if pids := sleeping(secs); len(pids) != 0 {
		t.Fatalf("the detached sleep %v outlived the confined command", pids)
	}
}

// A confined command stopped with SIGTERM, as the builder stops one past
// its time limit, leaves nothing it detached either.
func TestAStoppedConfinedCommandLeavesNoProcess(t *testing.T) {
	if _, err := exec.LookPath("setsid"); err != nil {
		t.Skip("no setsid here")
	}
	s := ready(t, Policy{})
	secs := uniqueSleep(t)
	script := `setsid sleep "$1" </dev/null >/dev/null 2>&1 & sleep 30`
	cmd, err := s.Confined(t.Context(), []string{resolvedTemp(t)}, []string{"sh", "-c", script, "sh", secs}, "/")
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	Release(cmd)
	ended := make(chan error, 1)
	go func() { ended <- cmd.Wait() }()
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	wait, stop := context.WithTimeout(t.Context(), 10*time.Second)
	defer stop()
	if !poll(wait, func() bool { return len(sleeping(secs)) > 0 }) {
		t.Fatal("the detached sleep never started")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ended:
		t.Logf("the helper ended: %v", err)
	case <-wait.Done():
		t.Fatal("the helper did not end on SIGTERM")
	}
	if pids := sleeping(secs); len(pids) != 0 {
		t.Fatalf("the detached sleep %v outlived the stopped command", pids)
	}
}

// ADR-0064, abuse test: from Landlock ABI 6 a confined command cannot
// signal outside its sandbox, so it cannot kill the helper that reaps it,
// nor any other process; host.exec's commands still can (ADR-0039).
func TestAConfinedCommandCannotSignalOutside(t *testing.T) {
	if abi, err := landlockABI(); err != nil || abi < 6 {
		t.Skip("no Landlock scopes here")
	}
	s := ready(t, Policy{})
	victim := exec.CommandContext(t.Context(), "sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = victim.Process.Kill(); _ = victim.Wait() })
	pid := strconv.Itoa(victim.Process.Pid)
	script := `kill -KILL "$PPID" 2>/dev/null; kill -KILL "$1" 2>/dev/null; echo survived`
	out, code := confined(t, s, []string{resolvedTemp(t)}, "sh", "-c", script, "sh", pid)
	if code != 0 || !strings.Contains(out, "survived") {
		t.Fatalf("the confined command killed its helper: exit %d, %q", code, out)
	}
	if err := victim.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatalf("the confined command killed a process outside: %v", err)
	}
	if out, code := output(t, s, "/", "kill", "-0", pid); code != 0 {
		t.Fatalf("host.exec's command cannot signal: exit %d, %q", code, out)
	}
}

// poll reports whether ok became true before ctx ended, asking on a ticker.
func poll(ctx context.Context, ok func() bool) bool {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !ok() {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
	return true
}

// ADR-0064, abuse test: Landlock does not govern a file's mode, owner or
// extended attributes, so the confined filter refuses changing them by path:
// a file outside, a denied file and a denied folder keep their modes. The
// same calls are refused inside the command's folder too. host.exec's
// commands keep them.
func TestAConfinedCommandChangesNoModeOrOwner(t *testing.T) {
	dir, db := secretDir(t)
	other := resolvedTemp(t)
	file := filepath.Join(other, "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	work := resolvedTemp(t)
	mine := filepath.Join(work, "mine")
	if err := os.WriteFile(mine, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := ready(t, Policy{Deny: func() []string { return []string{dir} }})
	owner := strconv.Itoa(os.Getuid())
	for _, argv := range [][]string{
		{"chmod", "0666", file}, {"chmod", "0666", db}, {"chmod", "0777", dir}, {"chmod", "0700", mine},
		{"chown", owner, file}, {"chown", owner, mine},
	} {
		if out, code := confined(t, s, []string{work}, argv...); code == 0 {
			t.Errorf("%q changed a file's metadata: %q", argv, out)
		}
	}
	for p, want := range map[string]os.FileMode{file: 0o600, db: 0o600, mine: 0o600} {
		if info, err := os.Stat(p); err != nil || info.Mode().Perm() != want {
			t.Errorf("%s: mode %v, %v; want %v", p, info.Mode().Perm(), err, want)
		}
	}
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() == 0o777 {
		t.Errorf("the denied folder's mode changed: %v, %v", info.Mode().Perm(), err)
	}
	if out, code := output(t, s, "/", "chmod", "0640", file); code != 0 {
		t.Fatalf("host.exec's command cannot chmod: exit %d, %q", code, out)
	}
}
