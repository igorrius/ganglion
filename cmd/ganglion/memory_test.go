//go:build linux

package main

import (
	"fmt"
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

// TestMemoryEcho measures the echo bot (principle 8: a node fits in
// 128 MB): RSS when idle, and RSS and peak after 1,000 messages through the
// fake Bot API. Run it with `make memory`; it is skipped otherwise.
func TestMemoryEcho(t *testing.T) { measure(t, "echo", nil) }

// TestMemoryAgent measures the agent with one model on a fake API: the
// history, sessions and turns trace grow while the model answers instantly.
func TestMemoryAgent(t *testing.T) {
	if os.Getenv("GANGLION_MEASURE") == "" {
		t.Skip("set GANGLION_MEASURE=1 (make memory)")
	}
	model := providertest.NewServer(t, "sk-measure")
	catalog := filepath.Join(t.TempDir(), "models.yaml")
	yaml := "providers:\n  - {name: fake, type: openai, base_url: " + model.URL() + ", key_env: FAKE_KEY}\n" +
		"models:\n  - {provider: fake, name: m, tier: smart, context: 64000}\n"
	if err := os.WriteFile(catalog, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	measure(t, "agent", []string{"GANGLION_MODELS_FILE=" + catalog, "FAKE_KEY=sk-measure", "GANGLION_DAILY_TURNS=0"})
}

func measure(t *testing.T, label string, extraEnv []string) {
	if os.Getenv("GANGLION_MEASURE") == "" {
		t.Skip("set GANGLION_MEASURE=1 (make memory)")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ganglion")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	fake := telegramtest.NewServer(t, crashToken)
	cmd := exec.Command(bin, "--roles", "core")
	cmd.Env = append(append(os.Environ(), extraEnv...),
		"GANGLION_DB="+filepath.Join(dir, "mem.db"),
		"TELEGRAM_BOT_TOKEN="+crashToken,
		"TELEGRAM_BOT_TOKEN_FILE=",
		"TELEGRAM_API_URL="+fake.URL(),
		"GANGLION_ADMIN=telegram:user:"+strconv.Itoa(adminID),
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()

	time.Sleep(2 * time.Second)
	idle := procKB(t, cmd.Process.Pid, "VmRSS")

	const n = 1000
	for i := range n {
		fake.AddMessage(telegramtest.Incoming{ChatID: adminID, UserID: adminID, Text: fmt.Sprintf("message %04d with some text in it", i)})
	}
	deadline := time.Now().Add(2 * time.Minute)
	for len(fake.Sent()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d echoes", len(fake.Sent()), n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(time.Second)
	after := procKB(t, cmd.Process.Pid, "VmRSS")
	peak := procKB(t, cmd.Process.Pid, "VmHWM")
	t.Logf("%s: binary %.1f MB; RSS idle %.1f MB; after %d messages %.1f MB, peak %.1f MB",
		label, float64(fi.Size())/(1<<20), float64(idle)/1024, n, float64(after)/1024, float64(peak)/1024)
	if peak > 128*1024 {
		t.Errorf("peak RSS %d kB is above 128 MB", peak)
	}
}

// procKB reads a "kB" field such as VmRSS from /proc/<pid>/status.
func procKB(t *testing.T, pid int, field string) int64 {
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
