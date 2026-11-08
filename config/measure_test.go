//go:build linux

package config

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/igorrius/ganglion/node"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/router/rules"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/telemetry"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
	"github.com/igorrius/ganglion/web"
	"github.com/samber/do/v2"
)

// hostileHost is where the hostile pages are served; only the measured
// node's resolver knows it.
const (
	hostileHost = "hostile.example.net"
	hostileAddr = "93.184.216.78"
	measureKey  = "sk-measure"
)

// measureNodeConfig is what TestMeasureNode is started with.
type measureNodeConfig struct {
	DB, Workspace, BotAPIURL, Catalog, Page string
}

// hostileShapes are the costliest 1 MiB pages for html2md (ADR-0035):
// dense tiny elements, which exhaust the module's memory, and a page at
// the depth cap's edge followed by 1 MiB of small elements.
func hostileShapes() map[string]string {
	const size = 1<<20 - 512
	fill := func(head, unit string) string {
		var b strings.Builder
		b.WriteString("<!doctype html><html><body>" + head)
		for b.Len()+len(unit) <= size {
			b.WriteString(unit)
		}
		return b.String()
	}
	deep := strings.Repeat("<div>", 122)
	return map[string]string{
		"dense-p":     fill("", "<p></p>"),
		"dense-li":    fill("<ul>", "<li>x"),
		"dense-links": fill("", `<a href="/a">a</a>`),
		"deep-table":  fill(deep, "<table></table>"),
		"deep-i":      fill(deep, "<i>x</i>"),
	}
}

// TestMemoryHostilePage measures the node's peak RSS (VmHWM) after one
// hostile 1 MiB page goes through web.fetch and html2md (Task 12, step 5),
// reported and not held to a line (principle 8, ADR-0056). Each page runs in
// a node of its own: this test binary started as TestMeasureNode, which builds the
// core container and the metrics as `ganglion run` does, with only
// web.fetch's resolver changed so the page's host leads to a local
// server. Run it with
// `make memory`; it is skipped otherwise.
func TestMemoryHostilePage(t *testing.T) {
	if os.Getenv("GANGLION_MEASURE") == "" {
		t.Skip("set GANGLION_MEASURE=1 (make memory)")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("the agent's memory needs git")
	}
	shapes := hostileShapes()
	for _, name := range []string{"dense-p", "dense-li", "dense-links", "deep-table", "deep-i"} {
		t.Run(name, func(t *testing.T) { measureHostile(t, shapes[name]) })
	}
}

func measureHostile(t *testing.T, html string) {
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(html))
	}))
	t.Cleanup(page.Close)
	model := providertest.NewServer(t, measureKey)
	url := "http://" + hostileHost + "/page"
	model.Respond(func(r providertest.Request) providertest.Answer {
		last := r.Messages[len(r.Messages)-1]
		switch {
		case len(r.Tools) == 0:
			return providertest.Answer{Text: "summary"}
		case last["role"] == "tool":
			content, _ := last["content"].(string)
			return providertest.Answer{Text: "done: " + strconv.Itoa(len(content)) + " bytes: " + content[:min(len(content), 160)]}
		}
		return providertest.Answer{Tool: "web.fetch", Args: `{"url":"` + url + `"}`}
	})
	fake := telegramtest.NewServer(t, crashTokenForMeasure)
	dir := t.TempDir()
	cfg, err := json.Marshal(measureNodeConfig{
		DB: filepath.Join(dir, "node.db"), Workspace: filepath.Join(dir, "workspace"), BotAPIURL: fake.URL(), Page: page.Listener.Addr().String(),
		Catalog: "providers:\n  - {name: fake, type: openai, base_url: " + model.URL() + ", key_env: FAKE_KEY}\n" +
			"models:\n  - {provider: fake, name: m, tier: smart, tools: true, context: 64000}\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestMeasureNode$")
	cmd.Env = append(os.Environ(), "GANGLION_MEASURE_NODE="+string(cfg))
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("node output:\n%s", out.String())
		}
	}()
	time.Sleep(2 * time.Second)
	idle := procStatusKB(t, cmd.Process.Pid, "VmRSS")
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "Read " + url})
	deadline := time.Now().Add(time.Minute)
	var reply string
	for reply == "" {
		for _, s := range fake.Sent() {
			if strings.HasPrefix(s.Text, "done: ") {
				reply = s.Text
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no answer after the fetch; sent %+v", fake.Sent())
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(time.Second)
	after := procStatusKB(t, cmd.Process.Pid, "VmRSS")
	peak := procStatusKB(t, cmd.Process.Pid, "VmHWM")
	t.Logf("page %d bytes: node RSS idle %.1f MB; after the page %.1f MB, peak %.1f MB; web.fetch gave %q",
		len(html), float64(idle)/1024, float64(after)/1024, float64(peak)/1024, reply[:min(len(reply), 140)])
}

// crashTokenForMeasure is the fake bot's token.
const crashTokenForMeasure = "7001:measure-secret"

// TestMeasureNode is the node TestMemoryHostilePage measures; it runs
// only when started by it, until SIGTERM.
func TestMeasureNode(t *testing.T) {
	raw := os.Getenv("GANGLION_MEASURE_NODE")
	if raw == "" {
		t.Skip("started by TestMemoryHostilePage")
	}
	var c measureNodeConfig
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	cat, err := rules.LoadCatalog(strings.NewReader(c.Catalog))
	if err != nil {
		t.Fatal(err)
	}
	b := Bootstrap{DB: c.DB, BotToken: crashTokenForMeasure, BotAPIURL: c.BotAPIURL, Admin: "telegram:user:42",
		Agent: AgentSettings{Catalog: &cat, Keys: map[string]string{"fake": measureKey}, Workspace: c.Workspace,
			DailyBudgetUSD: 1, TZ: time.UTC, SearchOff: true}}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM)
	defer stop()
	// Metrics as `ganglion run` serves them, so the node holds what a real
	// one does.
	tel, err := telemetry.Setup(ctx, telemetry.Config{Addr: "127.0.0.1:0", Service: "ganglion", Getenv: os.Getenv})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tel.Shutdown(context.Background()) }()
	logger := slog.New(telemetry.LogHandler(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	i := New(ctx, b, logger)
	n := acceptNet{host: hostileHost, addr: netip.MustParseAddr(hostileAddr), local: c.Page}
	do.Override(i, func(i do.Injector) (*web.Fetcher, error) {
		return web.NewFetcher(web.DefaultFetchConfig(), web.NewModuleConverter(do.MustInvoke[*runner.SwapTool](i)),
			web.WithResolver(n), web.WithConnect(n.connect)), nil
	})
	err = node.Run(ctx, []node.Role{node.Core}, func() ([]node.Component, error) { return do.Invoke[[]node.Component](i) }, logger)
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = i.ShutdownWithContext(sctx)
	if err != nil {
		t.Fatal(err)
	}
}

// procStatusKB reads a "kB" field such as VmRSS from /proc/<pid>/status.
func procStatusKB(t *testing.T, pid int, field string) int64 {
	t.Helper()
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, field+":"); ok {
			v, err := strconv.ParseInt(strings.TrimSuffix(strings.TrimSpace(rest), " kB"), 10, 64)
			if err != nil {
				t.Fatal(err)
			}
			return v
		}
	}
	t.Fatalf("%s not in /proc/%d/status", field, pid)
	return 0
}
