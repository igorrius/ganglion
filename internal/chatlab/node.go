//go:build lab

package chatlab

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/internal/rustenv"
	"github.com/igorrius/ganglion/logfile"
	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/secrets/vault"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/telemetry"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

const (
	labToken = "7001:lab-secret"
	labChat  = 42
	labOwner = "Igor"
	// pageAddr is public-looking, so the fetcher's address checks run on a
	// test page as on any other, and it leads to a local server.
	pageAddr = "93.184.216.77"
	// expirerPass is longer than two passes of the lab's expirer.
	expirerPass = 600 * time.Millisecond
	// Clocks.
	clockConfirmations = "confirmations"
	clockScheduler     = "scheduler"
)

// NodeOptions say what a lab node runs.
type NodeOptions struct {
	Models   settings.Models // ignored when Scenario.Model is ModelScripted
	Keys     map[string]string
	Relay    string // the ganglion binary the claude CLI starts as its MCP server
	Scenario Scenario
	Tokens   *Counter
}

// Node is the real core container with a fake Bot API on one side and real or
// scripted models on the other, in a temporary directory of its own.
type Node struct {
	dir, ws  string
	peer     *Telegram
	db       *sql.DB
	lab      *config.LabNode
	scripted *providertest.Server // the fake model of a scripted scenario
	mcp      *mcpLab              // the scenario's fake MCP server; nil for none
	cancel   context.CancelFunc
	done     chan error
	closers  *program
	stopOnce sync.Once
	stopErr  error
	logs     string
	pending  atomic.Int64 // how far the confirmations' clock is ahead, ns
	sched    atomic.Int64 // how far the scheduler's clock is ahead, ns
	tokens   *Counter     // counts the node's model calls
	calls0   int          // the counter's calls when this node started
}

// Start runs a node for o. Stop ends it and removes its directory.
func Start(ctx context.Context, o NodeOptions) (n *Node, err error) {
	dir, err := os.MkdirTemp("", "chatlab-")
	if err != nil {
		return nil, fmt.Errorf("chatlab: %w", err)
	}
	closers := &program{}
	n = &Node{dir: dir, ws: filepath.Join(dir, "workspace"), logs: filepath.Join(dir, "logs"), done: make(chan error, 1), closers: closers, tokens: o.Tokens}
	if o.Tokens != nil {
		n.calls0 = o.Tokens.Calls()
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("chatlab: %v", r)
		}
		if err != nil {
			closers.clean()
			_ = os.RemoveAll(dir)
			n = nil
		}
	}()
	if err := n.fillWorkspace(o.Scenario); err != nil {
		return nil, err
	}
	models, keys := o.Models, o.Keys
	if o.Scenario.Model == ModelScripted {
		models, keys, n.scripted = scriptedModel(closers, o.Scenario.Script, o.Scenario.Review, n.write)
	}
	return n, n.run(ctx, o, models, keys)
}

const (
	// labWriteWait bounds the wait for a Writing to reach the inbox; past it
	// the scenario's facts say what happened.
	labWriteWait = 10 * time.Second
)

// write puts w in the bot's chat and waits until the node has taken it into
// the inbox, so the model's answer that follows is overtaken by it.
func (n *Node) write(w Writing) {
	ctx, cancel := context.WithTimeout(context.Background(), labWriteWait)
	defer cancel()
	if w.Other {
		if err := n.lab.Allow(ctx, fmt.Sprintf("telegram:user:%d", labOther), labOtherName); err != nil {
			return
		}
	}
	n.peer.Write(w)
	for !n.peer.Taken() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// run builds the container and starts the node.
func (n *Node) run(ctx context.Context, o NodeOptions, models settings.Models, keys map[string]string) error {
	fake := telegramtest.NewServer(n.closers, labToken)
	if err := CheckLoopback(fake.URL()); err != nil {
		return err
	}
	loc, err := time.LoadLocation("Europe/Kyiv")
	if err != nil {
		return fmt.Errorf("chatlab: %w", err)
	}
	pages, mcpHTTP, err := n.serveFakes(o.Scenario)
	if err != nil {
		return err
	}
	b := config.Bootstrap{DB: filepath.Join(n.dir, "node.db"), BotToken: labToken, BotAPIURL: fake.URL(),
		Admin: fmt.Sprintf("telegram:user:%d", labChat), Build: rustenv.LocalFromEnv(os.Getenv), Secrets: labSecrets(), MasterKey: filepath.Join(n.dir, vault.KeyFile), MetricsAddr: "off", LogDir: n.logs, ModulesDir: filepath.Join(n.dir, "modules"),
		Agent: config.AgentSettings{Models: &models, Keys: keys, Workspace: n.ws, DailyBudgetUSD: 1, DailyTurns: 300,
			DailyTokens: 1_000_000, SessionIdle: 6 * time.Hour, TZ: loc, Memory: config.DefaultMemory()}, Timeouts: labTimeouts()}
	rot, err := logfile.Open(logfile.DefaultConfig(n.logs), time.Now)
	if err != nil {
		return fmt.Errorf("chatlab: logs: %w", err)
	}
	n.closers.Cleanup(func() { _ = rot.Close() })
	logger := slog.New(slog.NewJSONHandler(rot, &slog.HandlerOptions{Level: slog.LevelDebug}))

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	n.cancel = cancel
	n.closers.Cleanup(cancel)
	tel, err := telemetry.Setup(runCtx, telemetry.Config{Addr: "127.0.0.1:0", Service: "ganglion", Logger: logger})
	if err != nil {
		return fmt.Errorf("chatlab: telemetry: %w", err)
	}
	n.closers.Cleanup(func() {
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_ = tel.Shutdown(sctx)
	})
	b.Metrics = tel.Renderer()
	// Built here, not in the goroutine: the tables exist when Start returns,
	// and a container that cannot be built is Start's error.
	for name, cfg := range o.Scenario.Config {
		if err := settings.SetModule(filepath.Join(n.dir, settings.FileName), settings.Module{Name: "local/" + name, Config: cfg}); err != nil {
			return fmt.Errorf("chatlab: settings for %s: %w", name, err)
		}
	}
	mods, err := labModules(o.Scenario)
	if err != nil {
		return err
	}
	n.lab, err = config.NewLab(runCtx, b, logger, config.LabOptions{Models: models, Keys: keys, Relay: o.Relay, Resolver: pages, Connect: pages.connect, Modules: mods, MCPHTTP: mcpHTTP,
		PendingNow: func() time.Time { return time.Now().Add(time.Duration(n.pending.Load())) },
		SchedNow:   func() time.Time { return time.Now().Add(time.Duration(n.sched.Load())) },
		Wrap:       o.Tokens.Wrap})
	if err != nil {
		return fmt.Errorf("chatlab: %w", err)
	}
	go func() {
		n.done <- node.Run(runCtx, []node.Role{node.Core}, func() ([]node.Component, error) { return n.lab.Components, nil }, logger)
	}()
	n.db = n.lab.DB
	n.peer = NewTelegram(fake, labChat, labOwner)
	return nil
}

// serveFakes starts the scenario's web pages and its MCP server, and
// returns the way to each.
func (n *Node) serveFakes(s Scenario) (*pageNet, *http.Client, error) {
	pages, err := servePages(n.closers, s)
	if err != nil {
		return nil, nil, err
	}
	var hc *http.Client
	n.mcp, hc, err = serveMCP(n.closers, s.MCP)
	return pages, hc, err
}

// labSecrets is the node's secret source: the reader template's secret is
// set, to a placeholder that names no account, so its install window has
// nothing to refuse (the node never asks the service at install).
func labSecrets() secrets.Source {
	return secrets.Env{Getenv: func(name string) string {
		if name == "API_TOKEN" {
			return "lab-placeholder"
		}
		return ""
	}, ReadFile: func(string) ([]byte, error) { return nil, os.ErrNotExist }}
}

// fillWorkspace gives the workspace its persona and skills.
func (n *Node) fillWorkspace(s Scenario) error {
	persona := filepath.Join(s.Dir, s.Persona)
	if s.Persona == PersonaDefault {
		root, err := RepoRoot()
		if err != nil {
			return err
		}
		persona = filepath.Join(root, "examples", "workspace", "persona")
	}
	if err := copyDir(persona, filepath.Join(n.ws, "persona")); err != nil {
		return fmt.Errorf("chatlab: persona: %w", err)
	}
	for _, sk := range s.Skills {
		if err := copyDir(filepath.Join(s.Dir, sk), filepath.Join(n.ws, "skills", filepath.Base(sk))); err != nil {
			return fmt.Errorf("chatlab: skill %s: %w", sk, err)
		}
	}
	return nil
}

// RepoRoot is the repository this runs in: the nearest go.mod upwards.
func RepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("chatlab: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("chatlab: run this inside the repository")
		}
		dir = parent
	}
}

// copyDir copies a directory tree. A file keeps only whether it is executable:
// the other bits come from the umask of the checkout, and the node refuses a
// script the group may write.
func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		to := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(to, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o755
		}
		if err := os.WriteFile(to, b, mode); err != nil {
			return err
		}
		return os.Chmod(to, mode) // WriteFile keeps the mode of a file already there
	})
}

// scriptedModel starts a fake model that plays script, one step per request,
// and then says "ok". The memory check goes to a model of its own, so it
// never takes a step, and so does the memory review, which plays review
// and then answers that there is nothing to store.
func scriptedModel(owner *program, script []ScriptStep, review []string, write func(Writing)) (settings.Models, map[string]string, *providertest.Server) {
	const (
		key      = "scripted"
		provider = "script"
		guard    = "guard"    // the model of the memory check and the housekeeping
		reviewer = "reviewer" // the model of the memory review
	)
	fake := providertest.NewServer(owner, key)
	var mu sync.Mutex
	next, reviewed := 0, 0
	fake.Respond(func(r providertest.Request) providertest.Answer {
		if r.Model == guard {
			return providertest.Answer{Text: `{"ok":true,"reason":""}`}
		}
		if r.Model == reviewer {
			mu.Lock()
			defer mu.Unlock()
			if reviewed >= len(review) {
				return providertest.Answer{Text: `{"ops":[]}`}
			}
			reviewed++
			return providertest.Answer{Text: review[reviewed-1]}
		}
		mu.Lock()
		if next >= len(script) {
			mu.Unlock()
			return providertest.Answer{Text: "ok"}
		}
		st := script[next]
		next++
		mu.Unlock()
		if st.Writes != nil {
			write(*st.Writes)
		}
		if st.Hold > 0 {
			time.Sleep(st.Hold) // a model that takes its time, as a real one does
		}
		return providertest.Answer{Text: st.Say, Tool: st.Tool, Args: withJob(withBuild(st.Args, r), r)}
	})
	models := settings.Models{
		Providers: []settings.Provider{{Name: provider, Type: settings.TypeOpenAI, BaseURL: fake.URL()}},
		Models: []settings.ModelDef{
			{ID: ModelScripted, Provider: provider, Name: ModelScripted, Tools: true, Context: 200_000},
			{ID: guard, Provider: provider, Name: guard, Context: 200_000},
			{ID: reviewer, Provider: provider, Name: reviewer, Context: 200_000},
		},
		Purposes: map[router.Purpose][]settings.Choice{
			// The housekeeping purposes go to guard too, so they never take
			// a step of the script.
			router.PurposeChat:      {{Model: ModelScripted}},
			router.PurposeCheck:     {{Model: guard}},
			router.PurposeCompact:   {{Model: guard}},
			router.PurposeTranslate: {{Model: guard}},
			router.PurposeMemory:    {{Model: reviewer}},
		},
	}
	return models, map[string]string{provider: key}, fake
}

// pageNet serves a scenario's pages from a local server at a public-looking
// address; any other host is resolved and reached as in production.
type pageNet struct {
	hosts map[string]bool
	addr  netip.Addr
	local string
}

func servePages(owner *program, s Scenario) (*pageNet, error) {
	type page struct {
		kind string
		body []byte
	}
	byURL := map[string]page{}
	n := &pageNet{hosts: map[string]bool{}, addr: netip.MustParseAddr(pageAddr)}
	for _, p := range s.Pages {
		u, err := url.Parse(p.URL)
		if err != nil {
			return nil, fmt.Errorf("chatlab: page %s: %w", p.URL, err)
		}
		body, err := os.ReadFile(filepath.Join(s.Dir, p.File))
		if err != nil {
			return nil, fmt.Errorf("chatlab: page %s: %w", p.URL, err)
		}
		kind := mime.TypeByExtension(filepath.Ext(p.File))
		if kind == "" {
			kind = "text/plain; charset=utf-8"
		}
		byURL[strings.ToLower(u.Host)+u.RequestURI()] = page{kind, body}
		n.hosts[strings.ToLower(u.Hostname())] = true
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := byURL[strings.ToLower(r.Host)+r.URL.RequestURI()]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", p.kind)
		_, _ = w.Write(p.body)
	}))
	owner.Cleanup(srv.Close)
	n.local = srv.Listener.Addr().String()
	return n, nil
}

// LookupNetIP implements web.Resolver.
func (n *pageNet) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if n.hosts[strings.ToLower(host)] {
		return []netip.Addr{n.addr}, nil
	}
	return net.DefaultResolver.LookupNetIP(ctx, network, host)
}

func (n *pageNet) connect(ctx context.Context, network, addr string) (net.Conn, error) {
	if ap, err := netip.ParseAddrPort(addr); err == nil && ap.Addr() == n.addr {
		addr = n.local
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// Peer is the person's side of the chat.
func (n *Node) Peer() Peer { return n.peer }

// DB is the node's database.
func (n *Node) DB() *sql.DB { return n.db }

// Workspace is the agent's workspace.
func (n *Node) Workspace() string { return n.ws }

// LogDir is where the node's logs are.
func (n *Node) LogDir() string { return n.logs }

// Advance moves a clock forward by d. Moving the scheduler's also ticks it.
func (n *Node) Advance(clock string, d time.Duration) error {
	switch clock {
	case clockConfirmations:
		n.pending.Add(int64(d))
		time.Sleep(expirerPass)
	case clockScheduler:
		n.sched.Add(int64(d))
		if err := n.lab.Tick(context.Background()); err != nil {
			return fmt.Errorf("chatlab: tick: %w", err)
		}
	default:
		return fmt.Errorf("chatlab: clock %q", clock)
	}
	return nil
}

// busyQuery counts what the node has left to do: events in the inbox,
// messages in the outbox, background commands whose command.done event the
// node has not put yet, and background jobs still running or waiting (a
// module build, whose module.built event then resumes the conversation).
const busyQuery = `SELECT (SELECT count(*) FROM inbox WHERE status = 'pending') +
	(SELECT count(*) FROM outbox WHERE status = 'pending') +
	(SELECT count(*) FROM host_jobs WHERE background = 1 AND announced = 0) +
	(SELECT count(*) FROM background_jobs WHERE state IN ('queued', 'running'))`

// labTimeouts are the node's default timeouts, as the command line maps
// them from the settings.
func labTimeouts() config.Timeouts {
	a := settings.Default().Agent
	return config.Timeouts{Call: time.Duration(a.CallTimeout), Turn: time.Duration(a.TurnTimeout), Handler: a.HandlerTimeout()}
}

// Quiet waits until the node took the peer's last action and has nothing
// left to do (busyQuery) on three looks in a row.
func (n *Node) Quiet(ctx context.Context, limit time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	notQuiet := fmt.Errorf("chatlab: the node was not quiet after %v", limit)
	quiet := 0
	for quiet < 3 {
		select {
		case <-ctx.Done():
			return notQuiet
		case err := <-n.done:
			n.done <- err
			return fmt.Errorf("chatlab: the node stopped: %w", err)
		case <-tick.C:
		}
		if !n.peer.Taken() {
			quiet = 0
			continue
		}
		left, err := n.left(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return notQuiet
			}
			return fmt.Errorf("chatlab: quiet: %w", err)
		}
		if left > 0 {
			quiet = 0
			continue
		}
		quiet++
	}
	return nil
}

// left is what the node has left to do: busyQuery's rows and the notices of
// modules waiting on the bus, which no table shows yet.
func (n *Node) left(ctx context.Context) (int, error) {
	var left int
	if err := n.db.QueryRowContext(ctx, busyQuery).Scan(&left); err != nil {
		return 0, err
	}
	backlog, err := n.lab.Backlog(ctx)
	return left + backlog, err
}

// Stop ends the node and removes its directory. A second Stop does nothing.
func (n *Node) Stop(ctx context.Context) error {
	n.stopOnce.Do(func() {
		n.cancel()
		select {
		case err := <-n.done:
			n.stopErr = err
		case <-time.After(30 * time.Second):
			n.stopErr = errors.New("chatlab: the node did not stop")
		case <-ctx.Done():
			n.stopErr = ctx.Err()
		}
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		n.lab.Shutdown(sctx)
		n.closers.clean()
		_ = os.RemoveAll(n.dir)
	})
	return n.stopErr
}

// modelCalls is how many calls the node has made to a model since it started.
func (n *Node) modelCalls() int {
	if n.tokens == nil {
		return 0
	}
	return n.tokens.Calls() - n.calls0
}

// ReadView reads what a facts step checks; last is the model's words since
// the last action.
func ReadView(ctx context.Context, n *Node, last []Message) (View, error) {
	entries, err := n.lab.Journal.Entries(ctx)
	if err != nil {
		return View{}, fmt.Errorf("chatlab: journal: %w", err)
	}
	v := View{ModelCalls: n.modelCalls(), Journal: entries, Messages: n.peer.Messages(), Last: last, Memory: map[string]string{}, Jobs: map[string]int{},
		Exists: func(rel string) bool {
			_, err := os.Lstat(filepath.Join(n.dir, rel))
			return err == nil
		}, Deleted: n.deleted(), ModelSaw: n.modelSaw()}
	if err := n.db.QueryRowContext(ctx, `SELECT count(*) FROM inbox WHERE taken_by IS NOT NULL`).Scan(&v.Taken); err != nil {
		return View{}, fmt.Errorf("chatlab: inbox: %w", err)
	}
	if v.History, err = n.history(ctx); err != nil {
		return View{}, err
	}
	if v.Tainted, err = n.tainted(ctx); err != nil {
		return View{}, err
	}
	files, err := filepath.Glob(filepath.Join(n.ws, "memory", "*.md"))
	if err != nil {
		return View{}, fmt.Errorf("chatlab: memory: %w", err)
	}
	for _, f := range files {
		body, err := os.ReadFile(f)
		if err != nil {
			return View{}, fmt.Errorf("chatlab: memory: %w", err)
		}
		v.Memory[filepath.Base(f)] = string(body)
	}
	rows, err := n.db.QueryContext(ctx, `SELECT kind, count(*) FROM schedule_jobs WHERE deleted = 0 AND system = 0 GROUP BY kind`)
	if err != nil {
		return View{}, fmt.Errorf("chatlab: jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err != nil {
			return View{}, fmt.Errorf("chatlab: jobs: %w", err)
		}
		v.Jobs[kind] = count
	}
	if err := rows.Err(); err != nil {
		return View{}, fmt.Errorf("chatlab: jobs: %w", err)
	}
	return v, nil
}

// tainted is whether the owner's chat's session holds outside content; a chat
// with no state yet holds none.
func (n *Node) tainted(ctx context.Context) (bool, error) {
	var on bool
	err := n.db.QueryRowContext(ctx, `SELECT coalesce(json_extract(data, '$.tainted'), 0) FROM conv_state WHERE conv = ?`,
		fmt.Sprintf("telegram:%d", labChat)).Scan(&on)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("chatlab: taint: %w", err)
	}
	return on, nil
}

// history is the owner's chat as stored: its summary, then one line per
// message.
func (n *Node) history(ctx context.Context) (string, error) {
	var data string
	err := n.db.QueryRowContext(ctx, `SELECT data FROM conv_state WHERE conv = ?`, fmt.Sprintf("telegram:%d", labChat)).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("chatlab: history: %w", err)
	}
	var st struct {
		Summary  string `json:"summary"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal([]byte(data), &st); err != nil {
		return "", fmt.Errorf("chatlab: history: %w", err)
	}
	var b strings.Builder
	b.WriteString(st.Summary)
	for _, m := range st.Messages {
		fmt.Fprintf(&b, "\n%s: %s", m.Role, m.Content)
	}
	return b.String(), nil
}
