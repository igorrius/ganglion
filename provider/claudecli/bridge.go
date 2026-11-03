package claudecli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/igorrius/ganglion/mcp"
)

// The bridge's names and limits (ADR-0036).
const (
	serverName  = "ganglion"         // the MCP server's name in the CLI's config
	allowTools  = "mcp__ganglion__*" // every tool of that server, and nothing else
	socketName  = "mcp.sock"         // in the run dir
	configName  = "mcp.json"         // in the run dir
	relayArg    = "mcp-relay"        // the hidden relay command
	authTimeout = 2 * time.Second    // for the relay's first line
	maxAuth     = 16                 // connections authenticated at once
	tokenBytes  = 32                 // of crypto/rand, sent in hex
	maxToken    = 2*tokenBytes + 64  // the longest first line read
	runPattern  = "run-"             // os.MkdirTemp's pattern for run dirs
	tempPattern = "ganglion-mcp-"    // the same, under os.TempDir()
	privateMode = os.FileMode(0o600) // mcp.json and the socket
	// maxSocket is the longest socket path: sun_path holds 104 bytes on
	// macOS and 108 on Linux, the terminating NUL included.
	maxSocket = 103
)

// errSocketPath is a run dir whose socket path is too long even under
// os.TempDir().
var errSocketPath = errors.New("socket path too long")

// bridge serves one claude run's tools over MCP on a Unix socket in a
// private run dir (ADR-0036). The CLI starts `ganglion mcp-relay`, which
// connects and sends the run's token first; the bridge serves exactly one
// connection with the right token and closes every other one. close
// stops serving, waits for the serve goroutine and removes the run dir.
type bridge struct {
	dir    string // the run dir, mode 0700
	socket string
	config string // mcp.json, mode 0600, the only place the token is written
	token  string
	ln     *net.UnixListener
	server *mcp.Server
	auth   time.Duration

	cancel    context.CancelFunc
	done      chan struct{} // closed when the serve goroutine ends
	closeOnce sync.Once
}

// openBridge makes a run dir under workDir (under os.TempDir() when the
// socket path would be too long there), listens on its socket, writes
// the CLI's MCP config naming relay as the server's command, and starts
// serving. On an error nothing is left behind.
func openBridge(ctx context.Context, workDir, relay string, auth time.Duration, server *mcp.Server) (*bridge, error) {
	dir, err := runDir(workDir)
	if err != nil {
		return nil, err
	}
	b := &bridge{dir: dir, socket: filepath.Join(dir, socketName), config: filepath.Join(dir, configName),
		token: newToken(), server: server, auth: auth, done: make(chan struct{})}
	if err := b.listen(relay); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	ctx, b.cancel = context.WithCancel(ctx)
	go func() {
		defer close(b.done)
		b.serve(ctx)
	}()
	return b, nil
}

// runDir creates the run dir, mode 0700, where its socket path fits.
func runDir(workDir string) (string, error) {
	for _, parent := range []string{workDir, os.TempDir()} {
		pattern := runPattern
		if parent != workDir {
			pattern = tempPattern
		}
		// MkdirTemp adds up to 10 digits to the pattern.
		if len(filepath.Join(parent, pattern+"0123456789", socketName)) > maxSocket {
			continue
		}
		dir, err := os.MkdirTemp(parent, pattern) // mode 0700
		if err != nil {
			return "", fmt.Errorf("run dir: %w", err)
		}
		return dir, nil
	}
	return "", errSocketPath
}

// newToken is 32 bytes of crypto/rand in hex.
func newToken() string {
	b := make([]byte, tokenBytes)
	_, _ = rand.Read(b) // never fails; it crashes the program instead
	return hex.EncodeToString(b)
}

// listen opens the socket with mode 0600 and writes the config.
func (b *bridge) listen(relay string) error {
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: b.socket, Net: "unix"})
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	b.ln = ln
	// The run dir is 0700 already; the socket's own mode is a second lock.
	if err := os.Chmod(b.socket, privateMode); err != nil {
		_ = ln.Close()
		return fmt.Errorf("socket mode: %w", err)
	}
	cfg := map[string]any{"mcpServers": map[string]any{serverName: map[string]any{
		"command": relay,
		"args":    []string{relayArg, "--socket", b.socket},
		"env":     map[string]string{mcp.TokenEnv: b.token},
	}}}
	raw, _ := json.Marshal(cfg) // strings only
	if err := os.WriteFile(b.config, raw, privateMode); err != nil {
		_ = ln.Close()
		return fmt.Errorf("config: %w", err)
	}
	return nil
}

// args are the claude flags that give the run the bridge's tools and
// nothing else.
func (b *bridge) args() []string {
	return []string{"--mcp-config", b.config, "--allowedTools", allowTools}
}

// serve accepts connections until the listener closes. Each one is
// authenticated in its own goroutine, at most maxAuth at once, so a
// silent connection cannot hold the relay up; the first that sends the
// token is served and every other one is closed.
func (b *bridge) serve(ctx context.Context) {
	var (
		wg     sync.WaitGroup
		served atomic.Bool
		slots  = make(chan struct{}, maxAuth)
	)
	defer wg.Wait()
	for {
		conn, err := b.ln.AcceptUnix()
		if err != nil {
			return // closed by close
		}
		if served.Load() {
			_ = conn.Close()
			continue
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			_ = conn.Close()
			return
		}
		wg.Go(func() {
			r, ok := b.authenticate(ctx, conn)
			<-slots
			if !ok || !served.CompareAndSwap(false, true) {
				_ = conn.Close()
				return
			}
			b.session(ctx, conn, r)
		})
	}
}

// authenticate reads the first line, at most maxToken bytes within the
// auth timeout, and compares it with the token in constant time. The
// reader it returns holds whatever the relay sent after the token. The
// bridge closing ends the wait at once.
func (b *bridge) authenticate(ctx context.Context, conn *net.UnixConn) (io.Reader, bool) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	// An I/O deadline is wall-clock time by nature.
	if err := conn.SetReadDeadline(time.Now().Add(b.auth)); err != nil {
		return nil, false
	}
	r := bufio.NewReaderSize(conn, maxToken)
	line, err := r.ReadSlice('\n')
	if err != nil {
		return nil, false
	}
	ok := subtle.ConstantTimeCompare(bytes.TrimSuffix(line, []byte("\n")), []byte(b.token)) == 1
	if !ok || conn.SetReadDeadline(time.Time{}) != nil {
		return nil, false
	}
	return r, true
}

// session serves MCP on the authenticated connection until the relay
// closes it or the bridge closes.
func (b *bridge) session(ctx context.Context, conn *net.UnixConn, r io.Reader) {
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	// An error (a line over the limit, a broken connection) ends the
	// session; the CLI sees its server gone and the run goes on without
	// tools.
	_ = b.server.Serve(ctx, struct {
		io.Reader
		io.Writer
	}{r, conn})
}

// close stops serving: it cancels the running calls, closes the listener
// and the connection, waits for the serve goroutine and removes the run
// dir. It is safe to call more than once.
func (b *bridge) close() {
	b.closeOnce.Do(func() {
		b.cancel()
		_ = b.ln.Close()
		<-b.done
		_ = os.RemoveAll(b.dir)
	})
}
