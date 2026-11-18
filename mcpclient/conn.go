package mcpclient

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/igorrius/ganglion/internal/backoff"
	"github.com/igorrius/ganglion/tool"
)

// errCallFailed ends a server's watch: a call got no answer, so the server
// is unreachable until the next connect works.
var errCallFailed = errors.New("mcpclient: a call failed")

// serverConn is one configured server: its client, its standing and its
// approved copy of the tool list.
type serverConn struct {
	hub     *Hub
	cfg     Server
	client  *Client
	changed chan struct{} // the server said its list changed
	failed  chan struct{} // a call got no answer

	// started and cancel belong to the hub, which holds its lock for them.
	started bool
	cancel  context.CancelFunc

	mu       sync.RWMutex
	state    State
	tools    []Tool // the approved copy: listed tools that have a policy
	skipped  []Skip
	handlers []tool.Handler // the tools on offer, built when the list is taken
}

// newConn makes the server's state; the hub starts it.
func (h *Hub) newConn(cfg Server) *serverConn {
	s := &serverConn{hub: h, cfg: cfg, state: StateConnecting,
		changed: make(chan struct{}, 1), failed: make(chan struct{}, 1)}
	s.client = NewClient(cfg.Name, cfg.URL, cfg.Auth, cfg.Timeout,
		WithHTTPClient(h.hc), WithVersion(h.version), WithListChanged(s.signalChanged))
	return s
}

// stop ends the server's goroutine, if there is one.
func (s *serverConn) stop() {
	if s.cancel != nil {
		s.cancel()
	}
}

// signalChanged and signalFailed wake the watch; a signal already waiting
// is enough.
func (s *serverConn) signalChanged() { wake(s.changed) }
func (s *serverConn) signalFailed()  { wake(s.failed) }

func wake(c chan struct{}) {
	select {
	case c <- struct{}{}:
	default:
	}
}

// drain forgets the signals that came before a connect: it lists afresh.
func (s *serverConn) drain() {
	for _, c := range []chan struct{}{s.changed, s.failed} {
		select {
		case <-c:
		default:
		}
	}
}

// wait waits d, or until ctx ends; false then.
func (s *serverConn) wait(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-s.hub.after(d):
		return true
	}
}

// run connects, watches, and on any failure waits out the back-off and
// connects again, until ctx ends (R-MCP-8).
func (s *serverConn) run(ctx context.Context) {
	bo := backoff.New(s.hub.backoff)
	for ctx.Err() == nil {
		err := s.connect(ctx)
		if err == nil {
			bo.Reset()
			err = s.watch(ctx)
		}
		if ctx.Err() != nil {
			return
		}
		s.lose(ctx, err)
		if !s.wait(ctx, bo.Next()) {
			return
		}
	}
}

// connect is the handshake and the first list; the list is checked against
// the fingerprint (R-MCP-7).
func (s *serverConn) connect(ctx context.Context) error {
	s.drain()
	if err := s.client.Initialize(ctx); err != nil {
		return err
	}
	l, err := s.client.ListTools(ctx)
	if err != nil {
		return err
	}
	st, changed := s.apply(l)
	s.hub.in.connect(ctx, s.cfg.Name, connectOutcomeOf(st))
	s.announce(st, changed)
	return nil
}

// connectOutcomeOf is the metric outcome of a list taken in state st.
func connectOutcomeOf(st State) connectOutcome {
	if st == StateConnected {
		return connectOK
	}
	return connectChanged
}

// refresh lists again after a change and checks the list again.
func (s *serverConn) refresh(ctx context.Context) error {
	l, err := s.client.ListTools(ctx)
	if err != nil {
		return err
	}
	st, changed := s.apply(l)
	s.announce(st, changed)
	return nil
}

// watch listens for changes of the list until ctx ends or something fails:
// a re-list that fails, or a call that got no answer.
func (s *serverConn) watch(ctx context.Context) error {
	lctx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.listen(lctx)
	}()
	defer func() {
		stop()
		<-done
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.failed:
			return errCallFailed
		case <-s.changed:
			if err := s.refresh(ctx); err != nil {
				return err
			}
		}
	}
}

// listen reads the server's own stream, and again after it ends, until the
// server has none (405) or ctx ends.
func (s *serverConn) listen(ctx context.Context) {
	bo := backoff.New(s.hub.backoff)
	for ctx.Err() == nil {
		err := s.client.Listen(ctx)
		if errors.Is(err, ErrNoStream) || ctx.Err() != nil {
			return
		}
		if errors.Is(err, ErrUnauthorized) {
			// The credential no longer works: the watch ends and the
			// server waits for a new sign-in (R-MCP-6).
			s.callFailed(StateUnauthorized)
			return
		}
		if err == nil {
			bo.Reset()
		}
		if !s.wait(ctx, bo.Next()) {
			return
		}
	}
}

// apply takes a listed tool list: it is approved when its fingerprint is
// the admin's, and then its tools are on offer; otherwise none is
// (R-MCP-7), and the approved copy stays as it was, offered to nobody, so
// the tools a session has on still count against its limits. It returns the state and whether it is a change; the caller
// announces it, after it has counted what happened.
func (s *serverConn) apply(l ToolList) (st State, changed bool) {
	s.mu.RLock()
	st, tools, offered := StateToolsChanged, s.tools, []tool.Handler(nil)
	s.mu.RUnlock()
	if l.Fingerprint == s.cfg.Fingerprint {
		st = StateConnected
		tools = s.approved(l.Tools)
		offered = s.handlersOf(tools)
	}
	s.mu.Lock()
	changed = s.state != st
	s.state, s.tools, s.skipped, s.handlers = st, tools, l.Skipped, offered
	s.mu.Unlock()
	if changed && st == StateToolsChanged {
		s.hub.log.Warn("mcpclient: a server's tool list no longer matches the approved one", "server", s.cfg.Name)
	}
	return st, changed
}

// approved are the listed tools that have a policy.
func (s *serverConn) approved(listed []Tool) []Tool {
	var out []Tool
	for _, t := range listed {
		if _, ok := s.cfg.Policies[t.Short]; ok {
			out = append(out, t)
		}
	}
	return out
}

// lose records that the server cannot be used: it is unreachable, or needs
// a new sign-in, until a connect works. A call that failed has said so
// already.
func (s *serverConn) lose(ctx context.Context, err error) {
	if errors.Is(err, errCallFailed) {
		return
	}
	st, outcome := StateUnreachable, connectFailed
	if errors.Is(err, ErrUnauthorized) {
		st, outcome = StateUnauthorized, connectUnauthorized
	}
	s.hub.in.connect(ctx, s.cfg.Name, outcome)
	s.unavailable(st)
	s.hub.log.Warn("mcpclient: the server is not usable", "server", s.cfg.Name, "kind", kindOf(err))
}

// unavailable marks the server as in state st and takes its tools off offer.
func (s *serverConn) unavailable(st State) {
	s.mu.Lock()
	changed := s.state != st
	s.state, s.handlers = st, nil
	s.mu.Unlock()
	s.announce(st, changed)
}

// announce tells the hub's observer that the server is now in state st,
// when that is a change.
func (s *serverConn) announce(st State, changed bool) {
	if changed {
		s.hub.observe(s.cfg.Name, st)
	}
}

// kindOf is a closed word for the kind of an error, for logs.
func kindOf(err error) string {
	var se *StatusError
	switch {
	case errors.Is(err, ErrUnauthorized):
		return "unauthorized"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, ErrUnreachable):
		return "unreachable"
	case errors.Is(err, ErrTooLarge):
		return "too_large"
	case errors.Is(err, ErrRedirect):
		return "redirect"
	case errors.Is(err, ErrProtocol), errors.Is(err, ErrNoAnswer):
		return "protocol"
	case errors.As(err, &se):
		return "status"
	}
	return "other"
}

// status is the server's standing.
func (s *serverConn) status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Status{Name: s.cfg.Name, State: s.state, Tools: len(s.tools), Skipped: append([]Skip(nil), s.skipped...)}
}

// offered are the tools on offer: none unless the server is connected.
func (s *serverConn) offered() []tool.Handler {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.handlers
}

// snapshot is the state and the approved copy, for the tools that show it.
func (s *serverConn) snapshot() (State, []Tool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state, s.tools
}
