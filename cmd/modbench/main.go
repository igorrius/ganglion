// Command modbench measures a WASM module: module size,
// cold load time, per-call latency with a fresh instance, and the runner's
// memory. HTTP is answered from a replay file, so the numbers compare
// modules and toolchains rather than the network (P0, ADR-0011).
//
//	modbench -wasm bin/deye-reader.tinygo.wasm \
//	         -manifest modules/deye/deye-reader.yaml \
//	         -replay cmd/modbench/testdata/deye-replay.json \
//	         -config inverter_sn=2400000001 -n 200
//
// Run one variant per process: the memory figures are process-wide.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/signal"
	"runtime"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "modbench:", err)
		}
		os.Exit(1)
	}
}

type report struct {
	Wasm             string  `json:"wasm"`
	WasmBytes        int     `json:"wasm_bytes"`
	LoadMillis       float64 `json:"load_ms"`
	FirstCallMillis  float64 `json:"first_call_ms"`
	Calls            int     `json:"calls"`
	Failures         int     `json:"failures"`
	CallP50Micros    float64 `json:"call_p50_us"`
	CallP95Micros    float64 `json:"call_p95_us"`
	RSSBaseMiB       float64 `json:"rss_base_mib"`
	RSSAfterLoadMiB  float64 `json:"rss_after_load_mib"`
	RSSAfterCallsMiB float64 `json:"rss_after_calls_mib"`
	RSSPeakMiB       float64 `json:"rss_peak_mib"` // high-water mark, includes compilation
	HeapInuseMiB     float64 `json:"heap_inuse_mib"`
	LastResult       string  `json:"last_result"`
}

type kvFlag map[string]string

func (f kvFlag) String() string { return fmt.Sprint(map[string]string(f)) }
func (f kvFlag) Set(s string) error {
	k, v, ok := strings.Cut(s, "=")
	if !ok || k == "" {
		return fmt.Errorf("want key=value, got %q", s)
	}
	f[k] = v
	return nil
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("modbench", flag.ContinueOnError)
	fs.SetOutput(stderr)
	wasmPath := fs.String("wasm", "", "module .wasm file (required)")
	manifestPath := fs.String("manifest", "", "module manifest .yaml (required)")
	replayPath := fs.String("replay", "", "JSON object mapping URL paths to response bodies; __NOW__ becomes the current Unix time (required)")
	n := fs.Int("n", 100, "number of measured calls")
	config := kvFlag{}
	fs.Var(config, "config", "override a manifest config value, key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *wasmPath == "" || *manifestPath == "" || *replayPath == "" {
		fs.Usage()
		return errors.New("-wasm, -manifest and -replay are required")
	}

	wasm, err := os.ReadFile(*wasmPath)
	if err != nil {
		return err
	}
	src, err := os.ReadFile(*manifestPath)
	if err != nil {
		return err
	}
	m, err := runner.ParseManifest(src)
	if err != nil {
		return err
	}
	if m.Config == nil {
		m.Config = map[string]string{}
	}
	maps.Copy(m.Config, config)
	replaySrc, err := os.ReadFile(*replayPath)
	if err != nil {
		return err
	}
	replay, err := newReplay(replaySrc)
	if err != nil {
		return err
	}
	srv := httptest.NewServer(replay)
	defer srv.Close()

	// Every host name the module may reach is answered by the replay server.
	client := &http.Client{Transport: redirect(srv.URL), Timeout: m.Limits.Timeout}

	deps := runner.Deps{
		// Any granted secret gets a placeholder value: the replay server
		// accepts any credentials.
		Secrets: func(name string) (string, bool) { return "bench-" + strings.ToLower(name), true },
		State:   runner.NewMemState(),
		HTTP:    client,
		Logger:  slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelWarn})),
	}

	r := report{Wasm: *wasmPath, WasmBytes: len(wasm), Calls: *n}
	r.RSSBaseMiB = rssMiB()

	start := time.Now()
	inst, err := runner.Load(ctx, wasm, m, deps)
	if err != nil {
		return err
	}
	defer func() { _ = inst.Close(ctx) }()
	r.LoadMillis = millis(time.Since(start))
	r.RSSAfterLoadMiB = rssMiB()

	in := pdk.Input{Trigger: "cron"}
	start = time.Now()
	out, err := inst.Handle(ctx, in) // logs in and caches the token
	if err != nil {
		return fmt.Errorf("first call: %w", err)
	}
	if !out.OK {
		return fmt.Errorf("first call failed: %s %s", out.Kind, out.Error)
	}
	r.FirstCallMillis = millis(time.Since(start))

	durations := make([]time.Duration, 0, *n)
	for range *n {
		start := time.Now()
		out, err = inst.Handle(ctx, in)
		durations = append(durations, time.Since(start))
		if err != nil || !out.OK {
			r.Failures++
		}
	}
	r.LastResult = string(out.Result)
	slices.Sort(durations)
	r.CallP50Micros = micros(percentile(durations, 50))
	r.CallP95Micros = micros(percentile(durations, 95))

	runtime.GC()
	debug.FreeOSMemory()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	r.HeapInuseMiB = float64(ms.HeapInuse) / (1 << 20)
	r.RSSAfterCallsMiB = rssMiB()
	r.RSSPeakMiB = peakRSSMiB()

	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// replay answers requests by URL path with recorded bodies.
type replay struct {
	bodies map[string][]byte
}

const nowSentinel = "987654321987654321"

func newReplay(src []byte) (*replay, error) {
	// __NOW__ is not JSON; swap in a numeric sentinel to parse, then swap
	// the current time in on each request.
	src = bytes.ReplaceAll(src, []byte("__NOW__"), []byte(nowSentinel))
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(src, &raw); err != nil {
		return nil, fmt.Errorf("replay file: %w", err)
	}
	r := &replay{bodies: make(map[string][]byte, len(raw))}
	for path, body := range raw {
		r.bodies[path] = body
	}
	return r, nil
}

func (r *replay) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	body, ok := r.bodies[req.URL.Path]
	if !ok {
		http.NotFound(w, req)
		return
	}
	_, _ = io.Copy(io.Discard, req.Body)
	now := strconv.FormatInt(time.Now().Unix(), 10)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(bytes.ReplaceAll(body, []byte(nowSentinel), []byte(now)))
}

// redirect returns a transport that sends every request to target, keeping
// the path and query.
func redirect(target string) http.RoundTripper {
	u, err := url.Parse(target)
	if err != nil {
		panic(err)
	}
	base := http.DefaultTransport.(*http.Transport).Clone()
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		r := req.Clone(req.Context())
		r.URL.Scheme, r.URL.Host, r.Host = u.Scheme, u.Host, u.Host
		return base.RoundTrip(r)
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func percentile(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := (len(sorted)*p + 99) / 100
	return sorted[max(0, min(i, len(sorted))-1)]
}

// rssMiB reads the process's resident set size from /proc (Linux only).
func rssMiB() float64 {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return -1
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return -1
	}
	pages, _ := strconv.ParseFloat(f[1], 64)
	return pages * float64(os.Getpagesize()) / (1 << 20)
}

// peakRSSMiB reads the process's peak resident set size (VmHWM) from /proc.
func peakRSSMiB() float64 {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return -1
	}
	for line := range strings.SplitSeq(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "VmHWM:"); ok {
			f := strings.Fields(rest) // "12345 kB"
			if len(f) == 2 {
				kb, _ := strconv.ParseFloat(f[0], 64)
				return kb / 1024
			}
		}
	}
	return -1
}

func millis(d time.Duration) float64 { return float64(d.Microseconds()) / 1000 }
func micros(d time.Duration) float64 { return float64(d.Nanoseconds()) / 1000 }
