//go:build lab

package chatlab

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"time"

	"github.com/igorrius/ganglion/internal/redact"
)

// DefaultIdle is how long a session waits for a request before it ends.
const DefaultIdle = 2 * time.Hour

// SessionOptions say what a session runs.
type SessionOptions struct {
	Dir       string // from NewSessionDir; removed at the end
	Node      NodeOptions
	Idle      time.Duration // 0: DefaultIdle
	TurnLimit time.Duration // 0: DefaultTurnLimit
	Scenarios string        // where save writes
}

// session is a node and a person talking to it from the shell.
type session struct {
	o     SessionOptions
	n     *Node
	run   *repeatRun
	steps []Step
	stop  context.CancelFunc
	seen  atomic.Int64 // when the last request came, Unix nanoseconds
}

// RunSession keeps a node running for requests until a stop or too long a
// silence, then removes its directory.
func RunSession(ctx context.Context, o SessionOptions) error {
	defer func() { _ = os.RemoveAll(o.Dir) }()
	if o.Idle == 0 {
		o.Idle = DefaultIdle
	}
	if o.TurnLimit == 0 {
		o.TurnLimit = DefaultTurnLimit
	}
	n, err := Start(ctx, o.Node)
	if err != nil {
		return err
	}
	defer func() { _ = n.Stop(context.WithoutCancel(ctx)) }()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	s := &session{o: o, n: n, stop: cancel,
		run: &repeatRun{o: RunOptions{TurnLimit: o.TurnLimit, Catalog: o.Node.Catalog}, n: n}}
	s.seen.Store(time.Now().UnixNano())
	go s.watchIdle(ctx)
	return Serve(ctx, SocketIn(o.Dir), s.handle)
}

func (s *session) watchIdle(ctx context.Context) {
	tick := time.NewTicker(min(s.o.Idle/2, time.Minute))
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if time.Since(time.Unix(0, s.seen.Load())) > s.o.Idle {
				s.stop()
				return
			}
		}
	}
}

func (s *session) handle(r Request) Response {
	defer s.seen.Store(time.Now().UnixNano())
	ctx := context.Background()
	s.seen.Store(time.Now().UnixNano())
	var step Step
	switch r.Op {
	case OpSay:
		step = Step{Say: r.Text}
	case OpPress:
		step = Step{Press: r.Text}
	case OpSend:
		if r.File == nil {
			return Response{Error: "send needs a file"}
		}
		step = Step{Send: r.File}
	case OpAdvance:
		switch r.Clock {
		case clockConfirmations:
			step = Step{Advance: &Advance{Confirmations: r.For}}
		case clockScheduler:
			step = Step{Advance: &Advance{Scheduler: r.For}}
		default:
			return Response{Error: fmt.Sprintf("clock %q: confirmations or scheduler", r.Clock)}
		}
	case OpShow:
		out, err := s.show(ctx, r.What)
		if err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, Output: redact.String(out)}
	case OpSave:
		if err := SaveScenario(s.o.Scenarios, r.Name, s.steps); err != nil {
			return Response{Error: err.Error()}
		}
		return Response{OK: true, Output: "saved " + filepath.Join(s.o.Scenarios, r.Name+".yaml") + "; add its why and its facts or judge steps"}
	case OpStop:
		s.stop()
		return Response{OK: true, Output: "stopped"}
	default:
		return Response{Error: "unknown operation " + r.Op}
	}
	s.run.log.Reset()
	if err := s.run.step(ctx, step); err != nil {
		return Response{Output: redact.String(s.run.log.String()), Error: err.Error()}
	}
	s.steps = append(s.steps, step)
	return Response{OK: true, Output: redact.String(s.run.log.String())}
}

// show prints one view of the node.
func (s *session) show(ctx context.Context, what string) (string, error) {
	switch what {
	case "journal", "memory", "history":
		v, err := ReadView(ctx, s.n, nil)
		if err != nil {
			return "", err
		}
		switch what {
		case "journal":
			return journalLines(v), nil
		case "history":
			return v.History, nil
		}
		var b strings.Builder
		for name, body := range v.Memory {
			fmt.Fprintf(&b, "%s:\n%s\n", name, body)
		}
		return b.String(), nil
	case "turns":
		var b strings.Builder
		_, err := turnLines(ctx, s.n, &b)
		return b.String(), err
	case "logs":
		return readLogs(s.n.LogDir())
	}
	return "", fmt.Errorf("show %q: journal, turns, history, memory or logs", what)
}

// readLogs is the end of the node's newest log.
func readLogs(dir string) (string, error) {
	const keep = 200
	files, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		return "", fmt.Errorf("chatlab: logs: %w", err)
	}
	slices.Sort(files)
	var lines []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		lines = append(lines, strings.Split(strings.TrimSpace(string(b)), "\n")...)
	}
	if len(lines) > keep {
		lines = lines[len(lines)-keep:]
	}
	return strings.Join(lines, "\n"), nil
}
