//go:build unix

package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

// R-OBS-7, plan task 6 step 5: a seeded fault — the provider's key is
// wrong — is found through `ganglion mcp` alone, after the node stopped:
// turns.failed shows the provider's auth error (its 401) and config.show which key_env it
// reads.
func TestMCPDiagnosesAWrongKey(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ganglion")
	if out, err := exec.Command("go", "build", "-o", bin, "..").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fake := telegramtest.NewServer(t, crashToken)
	model := providertest.NewServer(t, "sk-right")
	models := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(models, []byte("providers:\n  - {name: or, type: openai, base_url: "+model.URL()+
		", key_env: OR_KEY}\nmodels:\n  - {provider: or, name: m1, tier: smart, tools: true, context: 64000}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	db := filepath.Join(dir, "fault.db")
	env := append(os.Environ(), "GANGLION_DB="+db, "GANGLION_LOG_DIR=", "GANGLION_MODELS_FILE="+models, "OR_KEY=sk-wrong",
		"OR_KEY_FILE=", "TELEGRAM_BOT_TOKEN="+crashToken, "TELEGRAM_BOT_TOKEN_FILE=", "TELEGRAM_API_URL="+fake.URL(),
		"GANGLION_ADMIN=telegram:user:"+strconv.Itoa(adminID), "GANGLION_METRICS_ADDR=off", "GANGLION_WORKSPACE="+filepath.Join(dir, "ws"))
	node := exec.Command(bin, "run", "--roles", "core")
	node.Env = env
	var nodeOut lockedBuffer
	node.Stdout, node.Stderr = &nodeOut, &nodeOut
	if err := node.Start(); err != nil {
		t.Fatal(err)
	}
	fake.AddMessage(telegramtest.Incoming{ChatID: adminID, UserID: adminID, Name: "Admin", Text: "hello"})
	deadline := time.Now().Add(30 * time.Second)
	for len(fake.Sent()) == 0 {
		if time.Now().After(deadline) {
			_ = node.Process.Kill()
			t.Fatalf("no reply:\n%s", nodeOut.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	_ = node.Process.Signal(syscall.SIGTERM)
	_ = node.Wait()

	for k, v := range map[string]string{"GANGLION_MODELS_FILE": models, "OR_KEY": "sk-wrong", "GANGLION_LOG_DIR": ""} {
		t.Setenv(k, v)
	}
	answers, stderr := mcpSession(t, []string{"--db", db}, 3, mcpInit,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"turns_failed","arguments":{}}}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"config_show","arguments":{}}}`)
	if len(answers) != 3 {
		t.Fatalf("answers %v\nstderr %s", answers, stderr)
	}
	failed, _ := json.Marshal(answers[1])
	if !strings.Contains(string(failed), `\"error\":\"auth\"`) || !strings.Contains(string(failed), `\"provider\":\"or\"`) {
		t.Fatalf("turns_failed does not show the provider's auth error: %s", failed)
	}
	cfg, _ := json.Marshal(answers[2])
	if !strings.Contains(string(cfg), "its key in OR_KEY") || strings.Contains(string(cfg), "sk-wrong") {
		t.Fatalf("config_show: %s", cfg)
	}
}
