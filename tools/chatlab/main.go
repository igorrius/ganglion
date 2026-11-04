//go:build lab

// Command chatlab tests the agent by talking to it (ADR-0048): real models or
// a scripted one, over the fake Bot API, with facts and a judge deciding.
//
//	go run -tags lab ./tools/chatlab run [names...]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/igorrius/ganglion/internal/chatlab"
	"github.com/igorrius/ganglion/provider/claudecli"
	"github.com/igorrius/ganglion/router/rules"
)

const usage = `usage: chatlab run [names...] [--max-tokens 200000] [--judge sonnet] [--models <file>] [--repeat n] [--dir <scenarios>]`

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
	default:
		err = fmt.Errorf("unknown command %q\n%s", args[0], usage)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "chatlab:", err)
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
	maxTokens := fs.Int("max-tokens", 200_000, "stop starting repeats past this many tokens")
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
	f, err := os.Open(*models)
	if err != nil {
		return 0, err
	}
	defer func() { _ = f.Close() }()
	cat, err := rules.LoadCatalog(f)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", *models, err)
	}
	keys, err := chatlab.KeysFor(cat)
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
