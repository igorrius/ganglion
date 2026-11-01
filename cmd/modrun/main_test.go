package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/pdk"
)

func TestParseEnv(t *testing.T) {
	src := "# comment\nA=1\n\nB = two words \nexport C=\"quoted\"\nD='single'\nEMPTY=\n"
	got, err := parseEnv(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parseEnv: %v", err)
	}
	want := map[string]string{"A": "1", "B": "two words", "C": "quoted", "D": "single", "EMPTY": ""}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d keys, want %d: %v", len(got), len(want), got)
	}
}

func TestParseEnvRejectsLinesWithoutEquals(t *testing.T) {
	if _, err := parseEnv(strings.NewReader("JUSTAKEY\n")); err == nil {
		t.Fatal("parseEnv accepted a line without '='")
	}
}

func TestRunModuleEndToEnd(t *testing.T) {
	dir := t.TempDir()
	wasm := filepath.Join(dir, "probe.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", wasm, "../../runner/runnertest/probe/own")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, b)
	}
	manifest := filepath.Join(dir, "probe.yaml")
	writeFile(t, manifest, "name: probe\nversion: 1\ncapabilities:\n  secret.get: [TOKEN]\n  state: true\n")
	envFile := filepath.Join(dir, ".env")
	writeFile(t, envFile, "TOKEN=from-env-file\nOTHER=hidden\n")
	state := filepath.Join(dir, "state.json")

	runOnce := func(payload string, extra ...string) pdk.Output {
		t.Helper()
		var stdout, stderr bytes.Buffer
		args := append([]string{"-wasm", wasm, "-manifest", manifest, "-env", envFile, "-state", state, "-payload", payload}, extra...)
		if err := run(context.Background(), args, &stdout, &stderr); err != nil {
			t.Fatalf("run %v: %v\n%s", args, err, stderr.String())
		}
		var out pdk.Output
		if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
			t.Fatalf("stdout is not an Output: %v\n%s", err, stdout.String())
		}
		return out
	}

	if out := runOnce(`{"op":"secret","name":"TOKEN"}`); !strings.Contains(string(out.Result), "from-env-file") {
		t.Errorf("secret result = %s, want the value from the env file", out.Result)
	}
	if out := runOnce(`{"op":"secret","name":"OTHER"}`); strings.Contains(string(out.Result), "hidden") {
		t.Errorf("ungranted secret leaked: %s", out.Result)
	}

	runOnce(`{"op":"state_put","name":"k","value":"kept"}`)
	if out := runOnce(`{"op":"state_get","name":"k"}`); !strings.Contains(string(out.Result), "kept") {
		t.Errorf("state did not survive between runs: %s", out.Result)
	}

	if out := runOnce(`{"op":"config","name":"inverter_sn"}`, "-config", "inverter_sn=42"); !strings.Contains(string(out.Result), `"42"`) {
		t.Errorf("-config override not applied: %s", out.Result)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestRunExtismModule(t *testing.T) {
	dir := t.TempDir()
	wasm := filepath.Join(dir, "probe.wasm")
	cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", wasm, "../../runner/runnertest/probe/extism")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, b)
	}
	manifest := filepath.Join(dir, "probe.yaml")
	writeFile(t, manifest, "name: probe\nversion: 1\n")
	var stdout, stderr bytes.Buffer
	args := []string{"-abi", "extism", "-wasm", wasm, "-manifest", manifest, "-env", filepath.Join(dir, "none"),
		"-state", filepath.Join(dir, "state.json"), "-payload", `{"op":"echo"}`}
	if err := run(context.Background(), args, &stdout, &stderr); err != nil {
		t.Fatalf("run: %v\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"trigger": "cron"`) && !strings.Contains(stdout.String(), `"trigger":"cron"`) {
		t.Fatalf("output = %s, want the echoed trigger", stdout.String())
	}
}
