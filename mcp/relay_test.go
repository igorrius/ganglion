package mcp

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// listen opens a Unix socket in a short temporary directory.
func listen(t *testing.T) (*net.UnixListener, string) {
	t.Helper()
	dir, err := os.MkdirTemp("", "mcp-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "s")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln, path
}

func TestRelaySendsTheTokenFirstAndCopiesLines(t *testing.T) {
	ln, path := listen(t)
	got := make(chan []string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- nil
			return
		}
		defer func() { _ = c.Close() }()
		r := bufio.NewReader(c)
		var lines []string
		for range 2 {
			l, _ := r.ReadString('\n')
			lines = append(lines, l)
		}
		_, _ = io.WriteString(c, `{"jsonrpc":"2.0","id":1,"result":{}}`+"\n")
		got <- lines
	}()
	var out strings.Builder
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
	if err := Relay(context.Background(), path, "tok", in, &out); err != nil {
		t.Fatalf("Relay: %v", err)
	}
	lines := <-got
	if len(lines) != 2 || lines[0] != "tok\n" || lines[1] != `{"jsonrpc":"2.0","id":1,"method":"ping"}`+"\n" {
		t.Fatalf("the socket got %q", lines)
	}
	if out.String() != `{"jsonrpc":"2.0","id":1,"result":{}}`+"\n" {
		t.Fatalf("stdout got %q", out.String())
	}
}

func TestRelayRefusesAnOversizeLine(t *testing.T) {
	ln, path := listen(t)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = io.Copy(io.Discard, c)
		_ = c.Close()
	}()
	in := strings.NewReader(strings.Repeat("x", MaxLine+1) + "\n")
	done := make(chan error, 1)
	go func() { done <- Relay(context.Background(), path, "tok", in, io.Discard) }()
	select {
	case err := <-done:
		if !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("Relay: %v, want ErrLineTooLong", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Relay did not return")
	}
}

func TestRelayWithoutASocketFails(t *testing.T) {
	err := Relay(context.Background(), filepath.Join(t.TempDir(), "none"), "tok", strings.NewReader(""), io.Discard)
	if err == nil {
		t.Fatal("Relay to a missing socket succeeded")
	}
}
