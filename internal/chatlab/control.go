package chatlab

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"
)

// Operations of a session.
const (
	OpPing    = "ping"
	OpSay     = "say"
	OpSend    = "send"
	OpPress   = "press"
	OpAdvance = "advance"
	OpShow    = "show"
	OpSave    = "save"
	OpStop    = "stop"
)

const (
	sessionPrefix = "chatlab-session-"
	socketName    = "sock"
	// maxSocketPath keeps a socket's path inside sun_path: 104 bytes on
	// macOS, 108 on Linux, the terminating NUL included.
	maxSocketPath = 100
	// startWait is how long start waits for the session's socket; the node
	// builds its container first.
	startWait = 30 * time.Second
)

// Request is one call to a session, as one line of JSON.
type Request struct {
	Op    string        `json:"op"`
	Text  string        `json:"text,omitempty"`
	File  *File         `json:"file,omitempty"`
	Clock string        `json:"clock,omitempty"`
	For   time.Duration `json:"for,omitempty"`
	What  string        `json:"what,omitempty"`
	Name  string        `json:"name,omitempty"`
}

// Response is a session's answer.
type Response struct {
	OK     bool   `json:"ok"`
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}

// NewSessionDir makes a session's directory, mode 0700 and hard to guess. A
// temporary directory too long for a socket's path gives way to /tmp.
func NewSessionDir() (string, error) {
	parent := os.TempDir()
	if len(parent)+len(sessionPrefix)+len("/0000000000/")+len(socketName) >= maxSocketPath {
		parent = "/tmp"
	}
	dir, err := os.MkdirTemp(parent, sessionPrefix)
	if err != nil {
		return "", fmt.Errorf("chatlab: %w", err)
	}
	return dir, nil
}

// SocketIn is the socket of the session in dir.
func SocketIn(dir string) string { return filepath.Join(dir, socketName) }

// Serve answers requests on a Unix socket, mode 0600, one at a time, until
// ctx ends. It answers a ping itself.
func Serve(ctx context.Context, sock string, h func(Request) Response) error {
	var lc net.ListenConfig
	l, err := lc.Listen(ctx, "unix", sock)
	if err != nil {
		return fmt.Errorf("chatlab: %w", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		_ = l.Close()
		return fmt.Errorf("chatlab: %w", err)
	}
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	var one sync.Mutex
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("chatlab: %w", err)
		}
		go func() {
			defer func() { _ = c.Close() }()
			var r Request
			if err := json.NewDecoder(c).Decode(&r); err != nil {
				return
			}
			if r.Op == OpPing {
				_ = json.NewEncoder(c).Encode(Response{OK: true})
				return
			}
			one.Lock()
			defer one.Unlock()
			_ = json.NewEncoder(c).Encode(h(r))
		}()
	}
}

// Call sends one request and waits for its answer, as long as a turn takes.
func Call(sock string, r Request) (Response, error) {
	var d net.Dialer
	c, err := d.DialContext(context.Background(), "unix", sock)
	if err != nil {
		return Response{}, fmt.Errorf("chatlab: %w", err)
	}
	defer func() { _ = c.Close() }()
	if err := json.NewEncoder(c).Encode(r); err != nil {
		return Response{}, fmt.Errorf("chatlab: %w", err)
	}
	var out Response
	if err := json.NewDecoder(bufio.NewReader(c)).Decode(&out); err != nil {
		return Response{}, fmt.Errorf("chatlab: the session did not answer: %w", err)
	}
	return out, nil
}

// BeginSession starts a session: spawn runs its server on a new directory,
// and the file pointer names that directory. A session already answering is
// left alone; what a dead one left behind is removed.
func BeginSession(pointer string, spawn func(dir string) error) (string, error) {
	return beginSession(pointer, spawn, startWait)
}

func beginSession(pointer string, spawn func(dir string) error, wait time.Duration) (string, error) {
	if old, err := os.ReadFile(pointer); err == nil {
		dir := strings.TrimSpace(string(old))
		if _, err := Call(SocketIn(dir), Request{Op: OpPing}); err == nil {
			return "", errors.New("chatlab: a session is running; chatlab stop ends it")
		}
		// The pointer's content decides what is removed, so only a session's
		// own directory goes.
		if strings.HasPrefix(filepath.Base(dir), sessionPrefix) {
			_ = os.RemoveAll(dir)
		}
		_ = os.Remove(pointer)
	}
	dir, err := NewSessionDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(pointer), 0o700); err != nil {
		return "", fmt.Errorf("chatlab: %w", err)
	}
	if err := os.WriteFile(pointer, []byte(dir), 0o600); err != nil {
		return "", fmt.Errorf("chatlab: %w", err)
	}
	if err := spawn(dir); err != nil {
		return "", err
	}
	deadline := time.Now().Add(wait)
	for {
		if _, err := Call(SocketIn(dir), Request{Op: OpPing}); err == nil {
			return dir, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("chatlab: the session did not start in %v; see %s", wait, filepath.Join(dir, "serve.log"))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// SaveScenario writes a session's steps as dir/name.yaml. It has no facts and
// its why is a TODO, so it does not load until someone says what it checks.
func SaveScenario(dir, name string, steps []Step) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("chatlab: scenario name %q must match %s", name, nameRE)
	}
	path := filepath.Join(dir, name+".yaml")
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("chatlab: %s exists", path)
	}
	list := make([]map[string]any, 0, len(steps))
	for _, s := range steps {
		switch {
		case s.Say != "":
			list = append(list, map[string]any{"say": s.Say})
		case s.Press != "":
			list = append(list, map[string]any{"press": s.Press})
		case s.Send != nil:
			list = append(list, map[string]any{"send": s.Send})
		case s.Advance != nil && s.Advance.Scheduler > 0:
			list = append(list, map[string]any{"advance": map[string]string{"scheduler": s.Advance.Scheduler.String()}})
		case s.Advance != nil:
			list = append(list, map[string]any{"advance": map[string]string{"confirmations": s.Advance.Confirmations.String()}})
		}
	}
	var body bytes.Buffer
	enc := yaml.NewEncoder(&body)
	enc.SetIndent(2)
	if err := enc.Encode(map[string]any{"name": name, "why": "TODO: the bug this scenario guards against", "steps": list}); err != nil {
		return fmt.Errorf("chatlab: %w", err)
	}
	const head = "# yaml-language-server: $schema=../../../schemas/chatlab-scenario.schema.json\n# Add facts or judge steps: without them this scenario does not load.\n"
	if err := os.WriteFile(path, append([]byte(head), body.Bytes()...), 0o644); err != nil { //nolint:gosec // a scenario is a file of the repository
		return fmt.Errorf("chatlab: %w", err)
	}
	return nil
}
