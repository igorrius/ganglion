//go:build lab

// Command chatlab tests the agent by talking to it (ADR-0048): real models or
// a scripted one, over the fake Bot API, with facts and a judge deciding.
//
//	go run -tags lab ./tools/chatlab run [names...]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/igorrius/ganglion/internal/chatlab"
	"github.com/igorrius/ganglion/provider/claudecli"
	"github.com/igorrius/ganglion/router/rules"
)

const usage = `usage:
  chatlab run [names...] [--max-tokens 1000000] [--judge sonnet] [--models <file>] [--repeat n] [--dir <scenarios>]
  chatlab start [--persona <dir>] [--models <file>]
  chatlab say "..."
  chatlab send <name> [--type <mime>] [--size <bytes>] [--caption "..."]
  chatlab press "Approve"
  chatlab advance confirmations|scheduler <duration>
  chatlab show journal|turns|history|memory|logs
  chatlab save <name>
  chatlab stop`

func main() {
	chatlab.ScrubEnvironment()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:])
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, usage)
		return 2
	}
	var err error
	code := 0
	switch args[0] {
	case "run":
		code, err = runScenarios(ctx, args[1:])
	case "start":
		err = startSession(args[1:])
	case "serve":
		err = serveSession(ctx, args[1:])
	case chatlab.OpSay, chatlab.OpPress, chatlab.OpSend, chatlab.OpAdvance, chatlab.OpShow, chatlab.OpSave, chatlab.OpStop:
		err = callSession(args[0], args[1:])
	default:
		err = fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "chatlab:", strings.TrimPrefix(err.Error(), "chatlab: "))
		return 2
	}
	return code
}

// flagsLast moves flags before the names, so `run a b --repeat 1` works.
func flagsLast(fs *flag.FlagSet, args []string) ([]string, error) {
	var names []string
	for len(args) > 0 {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) > 0 {
			names = append(names, args[0])
			args = args[1:]
		}
	}
	return names, nil
}

func runScenarios(ctx context.Context, args []string) (int, error) {
	root, err := chatlab.RepoRoot()
	if err != nil {
		return 0, err
	}
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	maxTokens := fs.Int("max-tokens", 1_000_000, "stop starting repeats past this many tokens")
	judge := fs.String("judge", chatlab.DefaultJudgeModel, "the judge's model, through the claude CLI")
	models := fs.String("models", filepath.Join(root, "models.example.yaml"), "the catalogue under test")
	repeat := fs.Int("repeat", 0, "repeats of each scenario instead of its own number")
	dir := fs.String("dir", filepath.Join(root, "tools", "chatlab", "scenarios"), "where the scenarios are")
	names, err := flagsLast(fs, args)
	if err != nil {
		return 0, err
	}
	scenarios, err := chatlab.LoadScenarios(*dir, names)
	if err != nil {
		return 0, err
	}
	cat, keys, err := loadCatalog(*models)
	if err != nil {
		return 0, err
	}
	tokens := &chatlab.Counter{}
	r := chatlab.RunAll(ctx, chatlab.RunOptions{Scenarios: scenarios, Catalog: cat, Keys: keys, Relay: filepath.Join(root, "bin", "ganglion"),
		Judge: chatlab.NewJudge(tokens.Wrap(claudecli.New("judge")), *judge), MaxTokens: *maxTokens, Repeat: *repeat, Tokens: tokens})
	path, err := chatlab.WriteReport(filepath.Join(root, "bin", "lab"), r)
	if err != nil {
		return 0, err
	}
	fmt.Print(r.Table())
	fmt.Println("Report:", path)
	return chatlab.ExitCode(r.Outcomes), nil
}

func loadCatalog(path string) (rules.Catalog, map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return rules.Catalog{}, nil, err
	}
	defer func() { _ = f.Close() }()
	cat, err := rules.LoadCatalog(f)
	if err != nil {
		return rules.Catalog{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	keys, err := chatlab.KeysFor(cat)
	return cat, keys, err
}

// sessionFlags are what start hands to the session's server.
func sessionFlags(fs *flag.FlagSet, root string) (persona, models *string) {
	persona = fs.String("persona", chatlab.PersonaDefault, "a directory with the persona's files")
	models = fs.String("models", filepath.Join(root, "models.example.yaml"), "the catalogue under test")
	return persona, models
}

func pointerFile(root string) string { return filepath.Join(root, "bin", "lab", "session") }

// startSession runs this program again, on its own, as the session's server.
func startSession(args []string) error {
	root, err := chatlab.RepoRoot()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	persona, models := sessionFlags(fs, root)
	if err := fs.Parse(args); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := chatlab.BeginSession(pointerFile(root), func(dir string) error {
		log, err := os.Create(filepath.Join(dir, "serve.log"))
		if err != nil {
			return err
		}
		defer func() { _ = log.Close() }()
		cmd := exec.Command(exe, "serve", "--dir", dir, "--persona", *persona, "--models", *models) //nolint:gosec,noctx // this program, outliving this call
		cmd.Stdout, cmd.Stderr, cmd.Dir = log, log, root
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := cmd.Start(); err != nil {
			return err
		}
		return cmd.Process.Release()
	})
	if err != nil {
		return err
	}
	fmt.Println("session running in", dir)
	return nil
}

func serveSession(ctx context.Context, args []string) error {
	root, err := chatlab.RepoRoot()
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	dir := fs.String("dir", "", "the session's directory")
	persona, models := sessionFlags(fs, root)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cat, keys, err := loadCatalog(*models)
	if err != nil {
		return err
	}
	scenario := chatlab.Scenario{Name: "session", Persona: chatlab.PersonaDefault}
	if *persona != chatlab.PersonaDefault {
		abs, err := filepath.Abs(*persona)
		if err != nil {
			return err
		}
		scenario.Dir, scenario.Persona = filepath.Dir(abs), filepath.Base(abs)
	}
	return chatlab.RunSession(ctx, chatlab.SessionOptions{Dir: *dir, Scenarios: filepath.Join(root, "tools", "chatlab", "scenarios"),
		Node: chatlab.NodeOptions{Catalog: cat, Keys: keys, Relay: filepath.Join(root, "bin", "ganglion"), Scenario: scenario, Tokens: &chatlab.Counter{}}})
}

// sessionRequest turns the command line into a request.
func sessionRequest(op string, args []string) (chatlab.Request, error) {
	r := chatlab.Request{Op: op}
	switch op {
	case chatlab.OpSay, chatlab.OpPress:
		if len(args) != 1 {
			return r, fmt.Errorf("%s takes one argument", op)
		}
		r.Text = args[0]
	case chatlab.OpShow:
		if len(args) != 1 {
			return r, errors.New("show takes journal, turns, history, memory or logs")
		}
		r.What = args[0]
	case chatlab.OpSave:
		if len(args) != 1 {
			return r, errors.New("save takes a name")
		}
		r.Name = args[0]
	case chatlab.OpAdvance:
		if len(args) != 2 {
			return r, errors.New("advance takes a clock and a duration")
		}
		d, err := time.ParseDuration(args[1])
		if err != nil {
			return r, err
		}
		r.Clock, r.For = args[0], d
	case chatlab.OpSend:
		fs := flag.NewFlagSet("send", flag.ContinueOnError)
		kind := fs.String("kind", chatlab.KindDocument, "document or photo")
		mime := fs.String("type", "application/pdf", "MIME type")
		size := fs.Int64("size", 1000, "bytes")
		caption := fs.String("caption", "", "the caption")
		names, err := flagsLast(fs, args)
		if err != nil {
			return r, err
		}
		if len(names) != 1 {
			return r, errors.New("send takes a name")
		}
		r.File = &chatlab.File{Kind: *kind, Name: names[0], Type: *mime, Size: *size, Caption: *caption}
	}
	return r, nil
}

func callSession(op string, args []string) error {
	root, err := chatlab.RepoRoot()
	if err != nil {
		return err
	}
	r, err := sessionRequest(op, args)
	if err != nil {
		return err
	}
	b, err := os.ReadFile(pointerFile(root))
	if err != nil {
		return errors.New("no session; chatlab start begins one")
	}
	got, err := chatlab.Call(chatlab.SocketIn(strings.TrimSpace(string(b))), r)
	if err != nil {
		return fmt.Errorf("no session answers; chatlab start begins one: %w", err)
	}
	if got.Output != "" {
		fmt.Println(got.Output)
	}
	if !got.OK {
		return errors.New(got.Error)
	}
	if op == chatlab.OpStop {
		return os.Remove(pointerFile(root))
	}
	return nil
}
