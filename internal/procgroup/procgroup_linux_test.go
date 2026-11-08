package procgroup

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"
)

// startOnAnEndingThread starts sleep 60 with Own from a goroutine that locks
// its thread and returns without unlocking, so Go ends that thread. The
// main thread never ends; a goroutine that lands there holds it while
// another starts the child, which then cannot land there.
func startOnAnEndingThread() (*exec.Cmd, error) {
	type started struct {
		cmd *exec.Cmd
		err error
	}
	out := make(chan started, 1)
	start := func() {
		runtime.LockOSThread() // never unlocked: the thread ends with the goroutine
		cmd := exec.CommandContext(context.Background(), "sleep", "60")
		Own(cmd)
		out <- started{cmd, cmd.Start()}
	}
	go func() {
		runtime.LockOSThread()
		if syscall.Gettid() != os.Getpid() {
			start()
			return
		}
		done := make(chan struct{})
		go func() { start(); close(done) }()
		<-done
		runtime.UnlockOSThread()
	}()
	s := <-out
	return s.cmd, s.err
}

// The child dies when the thread that started it ends: the death signal.
func TestTheChildDiesWithItsStartingThread(t *testing.T) {
	cmd, err := startOnAnEndingThread()
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan struct{})
	go func() { _ = cmd.Wait(); close(ended) }()
	t.Cleanup(func() { _ = cmd.Process.Kill(); <-ended })
	wait, stop := context.WithTimeout(t.Context(), 5*time.Second)
	defer stop()
	select {
	case <-ended:
	case <-wait.Done():
		t.Fatalf("the child %d outlived the thread that started it", cmd.Process.Pid)
	}
}
