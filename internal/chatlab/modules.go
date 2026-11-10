//go:build lab

package chatlab

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/igorrius/ganglion/config"
	"github.com/igorrius/ganglion/provider/providertest"
)

// BuildToken stands in a scripted tool call's arguments for the digest the
// last module.build answered, which no script can know in advance.
const BuildToken = "$BUILD"

var buildRE = regexp.MustCompile(`\\?"build\\?":\s*\\?"([0-9a-f]{64})`)

// withBuild is args with BuildToken replaced by the digest in the newest tool
// result of r that holds one; args is returned as it is when there is none.
func withBuild(args string, r providertest.Request) string {
	if !strings.Contains(args, BuildToken) {
		return args
	}
	for _, msg := range slices.Backward(r.Messages) {
		raw, err := json.Marshal(msg["content"])
		if err != nil {
			continue
		}
		// The content is JSON inside a JSON string: unescape one level.
		var text string
		if json.Unmarshal(raw, &text) != nil {
			text = string(raw)
		}
		if m := buildRE.FindStringSubmatch(text); m != nil {
			return strings.ReplaceAll(args, BuildToken, m[1])
		}
	}
	return args
}

var (
	probeOnce sync.Once
	probeWasm []byte
	probeErr  error
)

// probeModule builds the probe module once per process.
func probeModule() ([]byte, error) {
	probeOnce.Do(func() {
		root, err := RepoRoot()
		if err != nil {
			probeErr = err
			return
		}
		dir, err := os.MkdirTemp("", "chatlab-probe")
		if err != nil {
			probeErr = err
			return
		}
		defer func() { _ = os.RemoveAll(dir) }()
		out := filepath.Join(dir, "probe.wasm")
		cmd := exec.Command("go", "build", "-buildmode=c-shared", "-o", out, "./runner/runnertest/probe/wasm")
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
		if b, err := cmd.CombinedOutput(); err != nil {
			probeErr = fmt.Errorf("chatlab: build the probe module: %v\n%s", err, b)
			return
		}
		probeWasm, probeErr = os.ReadFile(out)
	})
	return probeWasm, probeErr
}

// labModules are the modules s installs from the start.
func labModules(s Scenario) ([]config.LabModule, error) {
	if len(s.Modules) == 0 {
		return nil, nil
	}
	wasm, err := probeModule()
	if err != nil {
		return nil, err
	}
	out := make([]config.LabModule, 0, len(s.Modules))
	for _, m := range s.Modules {
		manifest, err := os.ReadFile(filepath.Join(s.Dir, m.Manifest))
		if err != nil {
			return nil, fmt.Errorf("chatlab: module %s: %w", m.Name, err)
		}
		out = append(out, config.LabModule{Name: m.Name, Wasm: wasm, Manifest: manifest})
	}
	return out, nil
}
