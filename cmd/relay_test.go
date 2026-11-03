package cmd

import (
	"bufio"
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/mcp"
	"github.com/urfave/cli/v3"
)

func runRelay(t *testing.T, in io.Reader, out io.Writer, args ...string) error {
	t.Helper()
	root := &cli.Command{Name: "ganglion", Writer: io.Discard, ErrWriter: io.Discard,
		Commands: []*cli.Command{relayCommand(in, out)}}
	return root.Run(context.Background(), append([]string{"ganglion", "mcp-relay"}, args...))
}

func TestRelayCommandCarriesStdioWithTheTokenFirst(t *testing.T) {
	dir, err := os.MkdirTemp("", "relay-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	first := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			first <- ""
			return
		}
		defer func() { _ = c.Close() }()
		r := bufio.NewReader(c)
		tok, _ := r.ReadString('\n')
		line, _ := r.ReadString('\n')
		_, _ = io.WriteString(c, "echo "+line)
		first <- tok
	}()
	t.Setenv(mcp.TokenEnv, "secret-token")
	var out strings.Builder
	if err := runRelay(t, strings.NewReader("hello\n"), &out, "--socket", sock); err != nil {
		t.Fatalf("mcp-relay: %v", err)
	}
	if tok := <-first; tok != "secret-token\n" || out.String() != "echo hello\n" {
		t.Fatalf("token %q, stdout %q", tok, out.String())
	}
}

func TestRelayCommandNeedsTheToken(t *testing.T) {
	t.Setenv(mcp.TokenEnv, "")
	err := runRelay(t, strings.NewReader(""), io.Discard, "--socket", filepath.Join(t.TempDir(), "s"))
	if err == nil || !strings.Contains(err.Error(), mcp.TokenEnv) {
		t.Fatalf("without a token: %v", err)
	}
}

func TestRelayCommandIsHidden(t *testing.T) {
	_, _, out, _ := captured(t, "--help")
	if strings.Contains(out, "mcp-relay") {
		t.Fatalf("help lists the relay:\n%s", out)
	}
	// It is still there for the claude CLI to start.
	if _, _, _, err := captured(t, "mcp-relay", "--help"); err != nil {
		t.Fatalf("mcp-relay --help: %v", err)
	}
}

// The relay's stdout is the MCP stream the CLI reads: a usage error goes
// to stderr only, never as a line the CLI would parse (M3b final review).
func TestRelayUsageErrorsStayOffStdout(t *testing.T) {
	t.Setenv(mcp.TokenEnv, "t")
	for _, args := range [][]string{{"mcp-relay"}, {"mcp-relay", "--socket", "x", "--bogus"}} {
		var stdout, stderr strings.Builder
		err := newRoot(&stdout, &stderr, nil).Run(context.Background(), append([]string{"ganglion"}, args...))
		if err == nil || stdout.Len() != 0 {
			t.Errorf("%q: err %v, stdout %q", args, err, stdout.String())
		}
	}
}
