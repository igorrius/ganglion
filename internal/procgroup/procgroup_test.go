//go:build unix

package procgroup

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// gone reports whether pid has ended: no such process, or a zombie waiting
// for its new parent to reap it.
func gone(pid int) bool {
	if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
		return true
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return false // no /proc (macOS): Kill above is the answer
	}
	_, after, _ := strings.Cut(string(stat), ") ")
	return strings.HasPrefix(after, "Z")
}

// waitGone waits until pid has ended or ctx ends, polling on a ticker.
func waitGone(ctx context.Context, pid int) bool {
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for !gone(pid) {
		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
	return true
}

// A cancelled context kills the whole group, the child's children too.
func TestCancelKillsTheGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "sleep 60 & echo $!; wait")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	Own(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatalf("pid %q", line)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	cancel()
	_ = cmd.Wait()
	Kill(cmd)
	wait, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	if !waitGone(wait, pid) {
		t.Fatalf("the grandchild %d outlived its cancelled group", pid)
	}
}

// Kill after the child ended reaches what it left running.
func TestKillReachesWhatTheChildLeft(t *testing.T) {
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", "sleep 60 >/dev/null 2>&1 & echo $!")
	Own(cmd)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil {
		t.Fatalf("pid %q", out)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	Kill(cmd)
	wait, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	if !waitGone(wait, pid) {
		t.Fatalf("the child %d left behind outlived Kill", pid)
	}
}
