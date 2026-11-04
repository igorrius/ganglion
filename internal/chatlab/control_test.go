package chatlab

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func serveEcho(ctx context.Context, t *testing.T, sock string) {
	t.Helper()
	go func() {
		_ = Serve(ctx, sock, func(r Request) Response { return Response{OK: true, Output: "did " + r.Op} })
	}()
	waitFor(t, func() bool { _, err := os.Stat(sock); return err == nil })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func sessionTempDir(t *testing.T) string {
	t.Helper()
	dir, err := NewSessionDir()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestServeSocketMode(t *testing.T) {
	ctx := t.Context()
	dir := sessionTempDir(t)
	sock := SocketIn(dir)
	serveEcho(ctx, t, sock)
	info, err := os.Stat(sock)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("socket %v, %v", info, err)
	}
	d, err := os.Stat(dir)
	if err != nil || d.Mode().Perm() != 0o700 {
		t.Fatalf("directory %v, %v", d, err)
	}
	r, err := Call(sock, Request{Op: "say", Text: "hi"})
	if err != nil || !r.OK || r.Output != "did say" {
		t.Fatalf("response %+v, %v", r, err)
	}
}

func TestServeStopsWithItsContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	sock := SocketIn(sessionTempDir(t))
	go func() { done <- Serve(ctx, sock, func(Request) Response { return Response{OK: true} }) }()
	waitFor(t, func() bool { _, err := os.Stat(sock); return err == nil })
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not stop")
	}
}

func TestStartRefusesALiveSession(t *testing.T) {
	ctx := t.Context()
	dir := sessionTempDir(t)
	serveEcho(ctx, t, SocketIn(dir))
	pointer := filepath.Join(t.TempDir(), "session")
	if err := os.WriteFile(pointer, []byte(dir), 0o600); err != nil {
		t.Fatal(err)
	}
	spawned := false
	_, err := BeginSession(pointer, func(string) error { spawned = true; return nil })
	if err == nil || !strings.Contains(err.Error(), "running") || spawned {
		t.Fatalf("err %v, spawned %t", err, spawned)
	}
	if b, _ := os.ReadFile(pointer); string(b) != dir {
		t.Fatalf("the pointer changed to %q", b)
	}
}

func TestStartReplacesADeadSession(t *testing.T) {
	dead := sessionTempDir(t)
	if err := os.WriteFile(SocketIn(dead), nil, 0o600); err != nil { // no one listens on it
		t.Fatal(err)
	}
	pointer := filepath.Join(t.TempDir(), "session")
	if err := os.WriteFile(pointer, []byte(dead), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	dir, err := BeginSession(pointer, func(d string) error {
		go func() { _ = Serve(ctx, SocketIn(d), func(Request) Response { return Response{OK: true} }) }()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Fatalf("the dead session's directory stayed: %v", err)
	}
	if b, _ := os.ReadFile(pointer); string(b) != dir {
		t.Fatalf("the pointer holds %q", b)
	}
}

func TestStartLeavesAForeignDirectory(t *testing.T) {
	foreign := t.TempDir()
	keep := filepath.Join(foreign, "keep.txt")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	pointer := filepath.Join(t.TempDir(), "session")
	if err := os.WriteFile(pointer, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	dir, err := BeginSession(pointer, func(d string) error {
		go func() { _ = Serve(ctx, SocketIn(d), func(Request) Response { return Response{OK: true} }) }()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("a directory the pointer named, not a session's, was removed: %v", err)
	}
}

func TestStartReportsAServerThatDied(t *testing.T) {
	pointer := filepath.Join(t.TempDir(), "session")
	_, err := beginSession(pointer, func(string) error { return nil }, 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "did not start") {
		t.Fatalf("err %v", err)
	}
}

func TestSocketPathTooLong(t *testing.T) {
	long := filepath.Join(t.TempDir(), strings.Repeat("x", 120))
	if err := os.MkdirAll(long, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", long)
	dir := sessionTempDir(t)
	if l := len(SocketIn(dir)); l >= maxSocketPath {
		t.Fatalf("the socket path has %d bytes: %s", l, SocketIn(dir))
	}
}

func TestSaveRefusesTraversal(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"../x", "a/b", "", "A"} {
		if err := SaveScenario(dir, name, []Step{{Say: "hi"}}); err == nil {
			t.Errorf("%q was saved", name)
		}
	}
}

func TestSaveDoesNotOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mine.yaml")
	if err := os.WriteFile(path, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := SaveScenario(dir, "mine", []Step{{Say: "hi"}}); err == nil {
		t.Fatal("it overwrote a scenario")
	}
	if b, _ := os.ReadFile(path); string(b) != "mine" {
		t.Fatalf("file holds %q", b)
	}
}

func TestSavedScenarioCannotRunUntilEdited(t *testing.T) {
	dir := t.TempDir()
	steps := []Step{{Say: "hi"}, {Press: "Approve"}, {Send: &File{Kind: KindPhoto, Name: "a.jpg", Size: 9}}, {Advance: &Advance{Confirmations: 16 * time.Minute}}}
	if err := SaveScenario(dir, "mine", steps); err != nil {
		t.Fatal(err)
	}
	_, err := LoadScenario(filepath.Join(dir, "mine.yaml"))
	if err == nil || !strings.Contains(err.Error(), "cannot fail") {
		t.Fatalf("err %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "mine.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(b), "steps:\n", "steps:\n  - judge: holds\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "mine.yaml"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := LoadScenario(filepath.Join(dir, "mine.yaml"))
	if err != nil {
		t.Fatalf("after adding a judge: %v\n%s", err, edited)
	}
	if len(s.Steps) != 5 || s.Steps[1].Say != "hi" || s.Steps[2].Press != "Approve" || s.Steps[3].Send.Name != "a.jpg" || s.Steps[4].Advance.Confirmations != 16*time.Minute {
		t.Fatalf("steps %+v", s.Steps)
	}
}
