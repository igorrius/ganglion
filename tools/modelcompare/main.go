// Command modelcompare runs the same conversations through several models
// of a settings file's models section and writes a Markdown report: answers
// side by side, latency, tokens, cache and cost (M3a task 10, decision D-6).
//
//	modelcompare -models ganglion.local.yaml -prompts tools/modelcompare/prompts.yaml \
//	  -only claude:haiku,openrouter:deepseek/deepseek-v4.1-flash -out report.md
//
// Provider keys come from the variable each provider's key names (or
// <NAME>_FILE).
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/igorrius/ganglion/agent"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/provider/claudecli"
	"github.com/igorrius/ganglion/provider/openai"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/secrets"
	"github.com/igorrius/ganglion/settings"
)

// Conversation is a scripted conversation.
type Conversation struct {
	Name  string   `yaml:"name"`
	Turns []string `yaml:"turns"`
}

type promptFile struct {
	Conversations []Conversation `yaml:"conversations"`
}

// Answer is one model's answer to one turn.
type Answer struct {
	Text    string
	Err     string
	Latency time.Duration
	Usage   provider.Usage
	CostUSD float64
}

// Result is one model's run over all conversations.
type Result struct {
	Model   router.Model
	Answers [][]Answer // [conversation][turn]
}

// run plays every conversation through p, keeping history per
// conversation, with the agent's default system prompt.
func run(ctx context.Context, p provider.Provider, m router.Model, convs []Conversation, timeout time.Duration) Result {
	res := Result{Model: m}
	for _, c := range convs {
		history := []provider.Message{{Role: provider.RoleSystem, Content: agent.CoreRules + "\n\n" + agent.DefaultPersona}}
		var answers []Answer
		for _, turn := range c.Turns {
			history = append(history, provider.Message{Role: provider.RoleUser, Content: "Igor: " + turn})
			callCtx, cancel := context.WithTimeout(ctx, timeout)
			start := time.Now()
			resp, err := p.Chat(callCtx, provider.ChatRequest{Model: m.Name, Messages: history})
			cancel()
			a := Answer{Latency: time.Since(start), Usage: resp.Usage}
			if err != nil {
				a.Err = err.Error()
			} else {
				a.Text = resp.Message.Content
				a.CostUSD = (float64(resp.Usage.InputTokens)*m.Caps.PriceInMTok + float64(resp.Usage.OutputTokens)*m.Caps.PriceOutMTok) / 1e6
				history = append(history, provider.Message{Role: provider.RoleAssistant, Content: resp.Message.Content})
			}
			answers = append(answers, a)
		}
		res.Answers = append(res.Answers, answers)
	}
	return res
}

func median(ds []time.Duration) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	s := slices.Clone(ds)
	slices.Sort(s)
	return s[len(s)/2]
}

// report writes the Markdown report.
func report(out io.Writer, convs []Conversation, results []Result, when time.Time) error {
	var w strings.Builder
	fmt.Fprintf(&w, "Run %s, %d conversations, %d turns per model.\n\n", when.Format("2006-01-02 15:04 MST"), len(convs), turnCount(convs))
	fmt.Fprintln(&w, "| Model | Answered | Median latency | Input tokens (cached) | Output tokens | Cost | Cost per turn |")
	fmt.Fprintln(&w, "|---|---|---|---|---|---|---|")
	for _, r := range results {
		var lat []time.Duration
		ok, in, cached, out, cost := 0, 0, 0, 0, 0.0
		for _, conv := range r.Answers {
			for _, a := range conv {
				if a.Err == "" {
					ok++
					lat = append(lat, a.Latency)
				}
				in, cached, out, cost = in+a.Usage.InputTokens, cached+a.Usage.CacheReadTokens, out+a.Usage.OutputTokens, cost+a.CostUSD
			}
		}
		per := 0.0
		if ok > 0 {
			per = cost / float64(ok)
		}
		fmt.Fprintf(&w, "| %s/%s | %d/%d | %.1f s | %d (%d) | %d | $%.4f | $%.5f |\n",
			r.Model.Provider, r.Model.Name, ok, turnCount(convs), median(lat).Seconds(), in, cached, out, cost, per)
	}
	for ci, c := range convs {
		fmt.Fprintf(&w, "\n## %d. %s\n", ci+1, c.Name)
		for ti, turn := range c.Turns {
			fmt.Fprintf(&w, "\n**Igor:** %s\n\n", turn)
			for _, r := range results {
				a := r.Answers[ci][ti]
				text := strings.ReplaceAll(strings.TrimSpace(a.Text), "\n", "\n  > ")
				if a.Err != "" {
					text = "**error:** " + a.Err
				}
				fmt.Fprintf(&w, "- *%s/%s* (%.1f s):\n  > %s\n", r.Model.Provider, r.Model.Name, a.Latency.Seconds(), text)
			}
		}
	}
	_, err := io.WriteString(out, w.String())
	return err
}

func turnCount(convs []Conversation) int {
	n := 0
	for _, c := range convs {
		n += len(c.Turns)
	}
	return n
}

func main() {
	models := flag.String("models", "ganglion.local.yaml", "a settings file with a models section")
	prompts := flag.String("prompts", "tools/modelcompare/prompts.yaml", "conversations")
	only := flag.String("only", "", "comma-separated provider:model to compare (default: every model)")
	out := flag.String("out", "", "report file (default: stdout)")
	timeout := flag.Duration("timeout", 90*time.Second, "per call")
	flag.Parse()
	if err := mainErr(*models, *prompts, *only, *out, *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "modelcompare:", err)
		os.Exit(1)
	}
}

func mainErr(modelsPath, promptsPath, only, outPath string, timeout time.Duration) error {
	src, err := os.ReadFile(modelsPath)
	if err != nil {
		return err
	}
	file, err := settings.Parse(src)
	if err != nil {
		return fmt.Errorf("%s: %w", modelsPath, err)
	}
	if file.Models == nil {
		return fmt.Errorf("%s: no models section", modelsPath)
	}
	cat := *file.Models
	keys, err := cat.Keys(secrets.OSEnv())
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(promptsPath)
	if err != nil {
		return err
	}
	var pf promptFile
	if err := yaml.Unmarshal(raw, &pf); err != nil {
		return fmt.Errorf("%s: %w", promptsPath, err)
	}
	providers := map[string]provider.Provider{}
	for _, p := range cat.Providers {
		switch p.Type {
		case settings.TypeClaudeCLI:
			providers[p.Name] = claudecli.New(p.Name)
		default:
			providers[p.Name] = openai.New(p.Name, p.BaseURL, keys[p.Name])
		}
	}
	var picked []router.Model
	for _, m := range cat.RouterModels() {
		if only == "" || slices.Contains(strings.Split(only, ","), m.Provider+":"+m.Name) {
			picked = append(picked, m)
		}
	}
	if len(picked) == 0 {
		return fmt.Errorf("no model matches -only %q", only)
	}

	results := make([]Result, len(picked))
	var wg sync.WaitGroup
	for i, m := range picked {
		wg.Go(func() {
			fmt.Fprintf(os.Stderr, "running %s/%s\n", m.Provider, m.Name)
			results[i] = run(context.Background(), providers[m.Provider], m, pf.Conversations, timeout)
		})
	}
	wg.Wait()

	w := io.Writer(os.Stdout)
	if outPath != "" {
		of, err := os.Create(outPath)
		if err != nil {
			return err
		}
		defer func() { _ = of.Close() }()
		w = of
	}
	return report(w, pf.Conversations, results, time.Now())
}
