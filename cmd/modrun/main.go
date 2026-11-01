// Command modrun runs a WASM module once, outside a node: a development and
// acceptance tool for P0 (docs/spec.md, tasks 0.2–0.5).
//
//	modrun -wasm deye-reader.wasm -manifest modules/deye/deye-reader.yaml \
//	       -config inverter_sn=XXXX -env .env
//
// Secrets come from the env file and the process environment; the module
// sees only those its manifest grants. Module state persists in -state
// between runs. The module's Output is printed to stdout as JSON.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/igorrius/ganglion/pdk"
	"github.com/igorrius/ganglion/runner"
	"github.com/igorrius/ganglion/runner/extismrun"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, "modrun:", err)
		}
		os.Exit(1)
	}
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
	fs := flag.NewFlagSet("modrun", flag.ContinueOnError)
	fs.SetOutput(stderr)
	wasmPath := fs.String("wasm", "", "module .wasm file (required)")
	abi := fs.String("abi", "own", "host ABI: own or extism")
	manifestPath := fs.String("manifest", "", "module manifest .yaml (required)")
	envPath := fs.String("env", ".env", "file with secrets as KEY=VALUE lines; missing is fine")
	statePath := fs.String("state", ".ganglion/modrun-state.json", "module state file, kept between runs")
	trigger := fs.String("trigger", "cron", "trigger passed to the module")
	payload := fs.String("payload", "", "JSON payload passed to the module")
	config := kvFlag{}
	fs.Var(config, "config", "override a manifest config value, key=value (repeatable)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *wasmPath == "" || *manifestPath == "" {
		fs.Usage()
		return errors.New("-wasm and -manifest are required")
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
	if len(config) > 0 && m.Config == nil {
		m.Config = map[string]string{}
	}
	maps.Copy(m.Config, config)
	secrets, err := loadEnv(*envPath)
	if err != nil {
		return err
	}
	state, err := runner.LoadMemState(*statePath)
	if err != nil {
		return err
	}

	logger := slog.New(slog.NewTextHandler(stderr, nil))
	deps := runner.Deps{
		Secrets: func(name string) (string, bool) {
			if v, ok := os.LookupEnv(name); ok {
				return v, v != ""
			}
			v, ok := secrets[name]
			return v, ok && v != ""
		},
		State:  state,
		Logger: logger,
	}
	var mod interface {
		Handle(context.Context, pdk.Input) (pdk.Output, error)
		Close(context.Context) error
	}
	switch *abi {
	case "own":
		mod, err = runner.Load(ctx, wasm, m, deps)
	case "extism":
		mod, err = extismrun.Load(ctx, wasm, m, deps)
	default:
		return fmt.Errorf("unknown -abi %q (want own or extism)", *abi)
	}
	if err != nil {
		return err
	}
	defer func() { _ = mod.Close(ctx) }()

	start := time.Now()
	out, err := mod.Handle(ctx, pdk.Input{Trigger: *trigger, Payload: json.RawMessage(*payload)})
	logger.Info("call finished", "module", m.Name, "elapsed", time.Since(start).Round(time.Millisecond))
	if serr := saveState(state, *statePath); serr != nil {
		logger.Error("saving state failed", "err", serr)
	}
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

func saveState(s *runner.MemState, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return s.Save(path)
}

func loadEnv(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return parseEnv(f)
}

// parseEnv reads KEY=VALUE lines. Blank lines and # comments are skipped;
// an "export " prefix and matching surrounding quotes are removed.
func parseEnv(r io.Reader) (map[string]string, error) {
	env := map[string]string{}
	sc := bufio.NewScanner(r)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("env line %d: no '='", n)
		}
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		env[strings.TrimSpace(k)] = v
	}
	return env, sc.Err()
}
