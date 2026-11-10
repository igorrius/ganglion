package chatlab

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/settings"
)

// program is what the fakes need from their owner outside `go test`.
type program struct {
	mu   sync.Mutex
	done []func()
}

func (*program) Helper() {}

// Fatalf panics; Start recovers it into its error.
func (*program) Fatalf(format string, args ...any) { panic(fmt.Sprintf(format, args...)) }

func (p *program) Cleanup(f func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.done = append(p.done, f)
}

// clean runs what Cleanup stacked, newest first, once.
func (p *program) clean() {
	p.mu.Lock()
	done := p.done
	p.done = nil
	p.mu.Unlock()
	for _, f := range slices.Backward(done) {
		f()
	}
}

// Counter counts the tokens of a run, for its cap. It sits around every
// provider, so turns, the memory check and compaction all count.
type Counter struct{ n, calls atomic.Int64 }

// Wrap counts every call of p.
func (c *Counter) Wrap(p provider.Provider) provider.Provider { return counted{p, c} }

// Add counts n tokens spent outside the node, by the judge.
func (c *Counter) Add(n int) { c.n.Add(int64(n)) }

// Calls is how many model calls were counted so far.
func (c *Counter) Calls() int { return int(c.calls.Load()) }

// Total is the tokens counted so far.
func (c *Counter) Total() int { return int(c.n.Load()) }

type counted struct {
	provider.Provider
	c *Counter
}

func (p counted) Chat(ctx context.Context, req provider.ChatRequest) (provider.ChatResponse, error) {
	p.c.calls.Add(1)
	resp, err := p.Provider.Chat(ctx, req)
	p.c.Add(resp.Usage.InputTokens + resp.Usage.OutputTokens)
	return resp, err
}

// MainModel is the model under test: the provider's name of the first model
// of chat.
func MainModel(m settings.Models) string {
	chat := m.Purposes[router.PurposeChat]
	if len(chat) == 0 {
		return ""
	}
	for _, d := range m.Models {
		if d.ID == chat[0].Model {
			return d.Name
		}
	}
	return ""
}

// ScrubEnvironment drops every Telegram and node setting from this process
// and its children, so the lab cannot hold a real bot's token or find the
// live database.
func ScrubEnvironment() {
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "TELEGRAM_") || strings.HasPrefix(name, "GANGLION_") {
			_ = os.Unsetenv(name)
		}
	}
}

// KeysFor reads the providers' API keys from the environment; a Telegram
// variable is never a provider's key.
func KeysFor(m settings.Models) (map[string]string, error) {
	keys := map[string]string{}
	for _, p := range m.Providers {
		if p.Key == "" {
			continue
		}
		name := p.Key.Name()
		if strings.HasPrefix(name, "TELEGRAM_") {
			return nil, fmt.Errorf("chatlab: provider %s: key %s is a Telegram setting", p.Name, name)
		}
		key := os.Getenv(name)
		if key == "" {
			return nil, fmt.Errorf("chatlab: provider %s: %s is not set", p.Name, name)
		}
		keys[p.Name] = key
	}
	return keys, nil
}
