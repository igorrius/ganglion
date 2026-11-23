//go:build lab

package chatlab

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/node"
)

const (
	// edgeStopWait bounds the wait for the edge to stop.
	edgeStopWait = 30 * time.Second
	// edgeLinkWait bounds the wait for a restored link to be up again: the
	// leaf redials about once a second.
	edgeLinkWait = 30 * time.Second
	// edgeSettle is how long a count of the node's copy of the edge's events
	// is watched after it matches: two resyncs of the node's collector, so a
	// copy made twice shows as a count past the match.
	edgeSettle = 2 * time.Second
	// EdgePatience is how long a facts step waits for the edge's facts to
	// hold: the node copies the events of a restored link on its own timer,
	// and an edge takes a module after its check.
	EdgePatience = 60 * time.Second
)

// edgeLink is the link between the edge and the node's entry: a TCP relay on
// a loopback port that the scenario cuts and restores. A cut closes the
// listener and every connection through it, so the edge's leaf sees a link
// that is down, and the node a leaf that left.
type edgeLink struct {
	target string
	addr   string

	mu     sync.Mutex
	ln     net.Listener
	conns  map[net.Conn]struct{}
	closed bool
}

// newEdgeLink relays a loopback port to target, which is up from the start.
func newEdgeLink(target string) (*edgeLink, error) {
	l := &edgeLink{target: target, conns: map[net.Conn]struct{}{}}
	if err := l.listen("127.0.0.1:0"); err != nil {
		return nil, err
	}
	l.addr = l.ln.Addr().String()
	return l, nil
}

// URL is the address the edge's leaf dials.
func (l *edgeLink) URL() string { return "ws://" + l.addr }

func (l *edgeLink) listen(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("chatlab: the edge's link: %w", err)
	}
	l.mu.Lock()
	l.ln = ln
	l.mu.Unlock()
	go l.accept(ln)
	return nil
}

func (l *edgeLink) accept(ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		go l.relay(c)
	}
}

func (l *edgeLink) relay(c net.Conn) {
	up, err := net.DialTimeout("tcp", l.target, 5*time.Second)
	if err != nil {
		_ = c.Close()
		return
	}
	l.mu.Lock()
	if l.closed || l.ln == nil {
		l.mu.Unlock()
		_ = c.Close()
		_ = up.Close()
		return
	}
	l.conns[c], l.conns[up] = struct{}{}, struct{}{}
	l.mu.Unlock()
	done := make(chan struct{}, 2)
	pipe := func(dst, src net.Conn) {
		buf := make([]byte, 32<<10)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		done <- struct{}{}
	}
	go pipe(up, c)
	go pipe(c, up)
	<-done
	_ = c.Close()
	_ = up.Close()
	<-done
	l.mu.Lock()
	delete(l.conns, c)
	delete(l.conns, up)
	l.mu.Unlock()
}

// Cut takes the link down: nothing is accepted, and what runs is closed.
func (l *edgeLink) Cut() {
	l.mu.Lock()
	ln, conns := l.ln, l.conns
	l.ln, l.conns = nil, map[net.Conn]struct{}{}
	l.mu.Unlock()
	if ln != nil {
		_ = ln.Close()
	}
	for c := range conns {
		_ = c.Close()
	}
}

// Restore puts the link up again on the same port.
func (l *edgeLink) Restore() error {
	l.mu.Lock()
	up := l.ln != nil || l.closed
	l.mu.Unlock()
	if up {
		return nil
	}
	return l.listen(l.addr)
}

// Close ends the link for good.
func (l *edgeLink) Close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.Cut()
}

// labEdge is the edge that runs beside the node.
type labEdge struct {
	name   string
	e      *config.LabEdge
	link   *edgeLink
	cancel context.CancelFunc
	done   chan error
}

// StartEdge runs the edge that joined as a leaf of the node's embedded
// server, in this process: its own folder, database and bus, and a link the
// scenario can cut.
func (n *Node) StartEdge(ctx context.Context) error {
	switch {
	case n.edge != nil:
		return errors.New("chatlab: the edge runs already")
	case n.joined == nil:
		return errors.New("chatlab: no edge has joined: join_edge comes first")
	case n.lab.EdgeEntry() == "":
		return errors.New("chatlab: the node has no entry for edges: the scenario needs edge: true")
	}
	link, err := newEdgeLink(n.lab.EdgeEntry())
	if err != nil {
		return err
	}
	if err := os.MkdirAll(n.logs, 0o755); err != nil {
		link.Close()
		return fmt.Errorf("chatlab: %w", err)
	}
	logFile, err := os.Create(filepath.Join(n.logs, "edge.log"))
	if err != nil {
		link.Close()
		return fmt.Errorf("chatlab: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelDebug}))
	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	e, err := config.NewLabEdge(runCtx, config.LabEdgeOptions{Name: n.joined.Name, Creds: n.joined.Creds, URL: link.URL(),
		Dir: filepath.Join(n.dir, "edge"), Secrets: labSecrets(), Admin: fmt.Sprintf("telegram:user:%d", labChat)}, logger)
	if err != nil {
		cancel()
		link.Close()
		_ = logFile.Close()
		return fmt.Errorf("chatlab: the edge: %w", err)
	}
	le := &labEdge{name: n.joined.Name, e: e, link: link, cancel: cancel, done: make(chan error, 1)}
	go func() {
		le.done <- node.Run(runCtx, []node.Role{node.Edge}, func() ([]node.Component, error) { return e.Components, nil }, logger)
		_ = logFile.Close()
	}()
	n.edge = le
	return n.waitEdge(ctx, edgeLinkWait, "the edge's link to come up", func(ctx context.Context) bool { return le.e.Linked(ctx) })
}

// stopEdge ends the edge and its link.
func (n *Node) stopEdge() error {
	le := n.edge
	if le == nil {
		return nil
	}
	n.edge = nil
	le.cancel()
	var err error
	select {
	case err = <-le.done:
	case <-time.After(edgeStopWait):
		err = errors.New("chatlab: the edge did not stop")
	}
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	le.e.Shutdown(sctx)
	le.link.Close()
	return err
}

// EdgeLink cuts the edge's link, or restores it and waits until the edge is
// linked again.
func (n *Node) EdgeLink(ctx context.Context, how string) error {
	if n.edge == nil {
		return errors.New("chatlab: no edge runs")
	}
	if how == EdgeDown {
		n.edge.link.Cut()
		return n.waitEdge(ctx, edgeLinkWait, "the edge to see its link down", func(ctx context.Context) bool { return !n.edge.e.Linked(ctx) })
	}
	if err := n.edge.link.Restore(); err != nil {
		return err
	}
	return n.waitEdge(ctx, edgeLinkWait, "the edge's link to come up", func(ctx context.Context) bool { return n.edge.e.Linked(ctx) })
}

// EdgeEmit makes count events on the edge.
func (n *Node) EdgeEmit(ctx context.Context, count int) error {
	if n.edge == nil {
		return errors.New("chatlab: no edge runs")
	}
	return n.edge.e.Emit(ctx, count)
}

// InstallModule installs a new build of m, as the owner's install would.
func (n *Node) InstallModule(ctx context.Context, m Module, dir string) error {
	mods, err := labModules(Scenario{Dir: dir, Modules: []Module{m}})
	if err != nil {
		return err
	}
	return n.lab.InstallModule(ctx, mods[0])
}

// waitEdge polls cond until it holds or limit passes.
func (n *Node) waitEdge(ctx context.Context, limit time.Duration, what string, cond func(context.Context) bool) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	for !cond(ctx) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("chatlab: gave up waiting for %s after %v", what, limit)
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// ReadEdge reads what a facts step checks of the edge; nil when none runs.
func (n *Node) ReadEdge(ctx context.Context, want *EdgeFact) (*EdgeView, error) {
	le := n.edge
	if le == nil {
		return nil, nil
	}
	v := &EdgeView{Linked: le.e.Linked(ctx), Held: int(le.e.Held(ctx))}
	core, err := n.lab.EdgeEvents(ctx, le.name)
	if err != nil {
		return nil, fmt.Errorf("chatlab: the node's events of the edge: %w", err)
	}
	v.CoreHeld = int(core)
	if want == nil {
		return v, nil
	}
	if want.Running != nil {
		if v.Running, err = le.e.Running(ctx, want.Running.Module); err != nil {
			return nil, fmt.Errorf("chatlab: the edge's modules: %w", err)
		}
	}
	if want.Actual != nil {
		a, ok, err := n.lab.EdgeActual(ctx, le.name, want.Actual.Module)
		if err != nil {
			return nil, fmt.Errorf("chatlab: the edge's actual key: %w", err)
		}
		if ok {
			v.Actual = &ActualView{Result: a.Result, Version: a.Version}
		}
	}
	return v, nil
}
