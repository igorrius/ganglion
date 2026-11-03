//go:build linux || darwin

package claudecli

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider"
)

// gone reports whether pid has ended: no such process, or a zombie
// waiting for its new parent to reap it.
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

// Cancelling a run kills claude's whole process group, so its relay
// does not stay behind.
func TestCancelKillsTheProcessGroup(t *testing.T) {
	c, rec := fake(t, "spawn")
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(500 * time.Millisecond); cancel() }()
	if _, err := c.Chat(ctx, provider.ChatRequest{Model: "haiku", Messages: history}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Chat: %v", err)
	}
	raw, err := os.ReadFile(rec)
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(string(raw)), "child "))
	if err != nil {
		t.Fatalf("record %q", raw)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	deadline := time.Now().Add(3 * time.Second)
	for !gone(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("the child %d of a cancelled claude is still running", pid)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
