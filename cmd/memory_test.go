//go:build linux

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

// TestMemoryEcho measures the echo bot: RSS when idle, and RSS and peak
// after 1,000 messages through the fake Bot API. Small hosts are a target,
// not a line (principle 8, ADR-0056), so the numbers are reported and
// compared by hand with the previous milestone. Run it with `make memory`;
// it is skipped otherwise.
func TestMemoryEcho(t *testing.T) { measure(t, "echo", "", everyMessage) }

// TestMemoryAgent measures the agent with one model on a fake API: the
// history, sessions and turns trace grow while the model answers instantly.
func TestMemoryAgent(t *testing.T) {
	if os.Getenv("GANGLION_MEASURE") == "" {
		t.Skip("set GANGLION_MEASURE=1 (make memory)")
	}
	model := providertest.NewServer(t, "sk-measure")
	measure(t, "agent", "agent: {limits: {daily_turns: off}}\n"+fakeModels(model.URL(), "FAKE_KEY"), everyMessage)
}

// measureTools are the tools the fake model calls in turn, one per turn:
// reads the agent answers without asking the owner.
var measureTools = [][2]string{
	{"schedule.list", `{}`},
	{"cells.get", `{"id":"load.boiler"}`},
	{"memory.read", `{}`},
	{"memory.recall", `{"query":"message"}`},
}

// TestMemoryAgentTools measures the agent when every turn calls a tool:
// the model asks for one, reads its result and answers, so each turn
// writes the tool-call journal and two model calls (Task 12, step 5).
func TestMemoryAgentTools(t *testing.T) {
	if os.Getenv("GANGLION_MEASURE") == "" {
		t.Skip("set GANGLION_MEASURE=1 (make memory)")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("memory needs git")
	}
	model := providertest.NewServer(t, "sk-measure")
	var turn atomic.Int64
	model.Respond(func(r providertest.Request) providertest.Answer {
		last := r.Messages[len(r.Messages)-1]
		switch {
		case len(r.Tools) == 0: // compaction
			return providertest.Answer{Text: "summary"}
		case last["role"] == "tool":
			return providertest.Answer{Text: "answer " + strconv.FormatInt(turn.Load(), 10)}
		}
		k := turn.Add(1)
		call := measureTools[int(k)%len(measureTools)]
		return providertest.Answer{Tool: call[0], Args: call[1]}
	})
	answers := func(sent []telegramtest.Outgoing) int {
		n := 0
		for _, s := range sent {
			if strings.HasPrefix(s.Text, "answer ") {
				n++
			}
		}
		return n
	}
	measure(t, "agent with tools", "agent: {limits: {daily_turns: off, daily_tokens: off}, search: {mode: off}}\n"+
		fakeModels(model.URL(), "FAKE_KEY"), answers)
}

// TestMemoryAgentHostExec measures the agent when every turn's tool is
// `host.exec df -h` through the real command sandbox: a helper process per
// call and the output captured (M3d, Task 12). It skips where the sandbox is
// not ready, which shows as a first result without df's header.
func TestMemoryAgentHostExec(t *testing.T) {
	if os.Getenv("GANGLION_MEASURE") == "" {
		t.Skip("set GANGLION_MEASURE=1 (make memory)")
	}
	model := providertest.NewServer(t, "sk-measure")
	var turn atomic.Int64
	var notReady atomic.Bool
	model.Respond(func(r providertest.Request) providertest.Answer {
		last := r.Messages[len(r.Messages)-1]
		switch {
		case len(r.Tools) == 0: // compaction
			return providertest.Answer{Text: "summary"}
		case last["role"] == "tool":
			if c, _ := last["content"].(string); !strings.Contains(c, "Filesystem") {
				notReady.Store(true)
			}
			return providertest.Answer{Text: "answer " + strconv.FormatInt(turn.Load(), 10)}
		}
		turn.Add(1)
		return providertest.Answer{Tool: "host.exec", Args: `{"argv":["df","-h"],"reason":"measure"}`}
	})
	answers := func(sent []telegramtest.Outgoing) int {
		if notReady.Load() {
			t.Skip("the command sandbox is not ready on this machine: host.exec df -h gave no table")
		}
		n := 0
		for _, s := range sent {
			if strings.HasPrefix(s.Text, "answer ") {
				n++
			}
		}
		return n
	}
	measure(t, "agent with host.exec", "agent: {limits: {daily_turns: off, daily_tokens: off}, search: {mode: off}}\n"+
		fakeModels(model.URL(), "FAKE_KEY"), answers)
}

// TestMemoryMetrics measures the picture: RSS and peak after 50 /metrics
// calls, each rendering the dashboard's history to a PNG (M3c, task 9).
func TestMemoryMetrics(t *testing.T) {
	if os.Getenv("GANGLION_MEASURE") == "" {
		t.Skip("set GANGLION_MEASURE=1 (make memory)")
	}
	model := providertest.NewServer(t, "sk-measure")
	cmd, fake, size := startMeasured(t, "agent: {limits: {daily_turns: off, daily_tokens: off}}\n"+fakeModels(model.URL(), "FAKE_KEY"))
	defer stopMeasured(cmd)
	time.Sleep(2 * time.Second)
	const n = 50
	for range n {
		fake.AddMessage(telegramtest.Incoming{ChatID: adminID, UserID: adminID, Text: "/metrics"})
	}
	deadline := time.Now().Add(5 * time.Minute)
	for len(fake.Uploads()) < n {
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d pictures", len(fake.Uploads()), n)
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(time.Second)
	after, peak := procKB(t, cmd.Process.Pid, "VmRSS"), procKB(t, cmd.Process.Pid, "VmHWM")
	t.Logf("metrics: binary %.1f MB; after %d pictures RSS %.1f MB, peak %.1f MB", float64(size)/(1<<20), n, float64(after)/1024, float64(peak)/1024)
}

// everyMessage counts every message sent as one reply.
func everyMessage(sent []telegramtest.Outgoing) int { return len(sent) }

// measure starts the binary, reads its RSS when idle, sends 1,000
// messages and reads RSS and peak once replies counts all of them.
func measure(t *testing.T, label, config string, replies func([]telegramtest.Outgoing) int) {
	if os.Getenv("GANGLION_MEASURE") == "" {
		t.Skip("set GANGLION_MEASURE=1 (make memory)")
	}
	cmd, fake, size := startMeasured(t, config)
	defer stopMeasured(cmd)

	time.Sleep(2 * time.Second)
	idle := procKB(t, cmd.Process.Pid, "VmRSS")

	// One at a time, each after the last reply: messages sent at once would join
	// one turn (R-LIVE-1), and this measures 1,000 turns.
	const n = 1000
	deadline := time.Now().Add(5 * time.Minute)
	for i := range n {
		fake.AddMessage(telegramtest.Incoming{ChatID: adminID, UserID: adminID, Text: fmt.Sprintf("message %04d with some text in it", i)})
		for replies(fake.Sent()) <= i {
			if time.Now().After(deadline) {
				t.Fatalf("only %d of %d replies", replies(fake.Sent()), n)
			}
			time.Sleep(time.Millisecond)
		}
	}
	time.Sleep(time.Second)
	after := procKB(t, cmd.Process.Pid, "VmRSS")
	peak := procKB(t, cmd.Process.Pid, "VmHWM")
	t.Logf("%s: binary %.1f MB; RSS idle %.1f MB; after %d messages %.1f MB, peak %.1f MB",
		label, float64(size)/(1<<20), float64(idle)/1024, n, float64(after)/1024, float64(peak)/1024)
}

// startMeasured builds and starts the binary on a fake Bot API with config
// added to its ganglion.yaml; it returns the binary's size.
func startMeasured(t *testing.T, config string) (*exec.Cmd, *telegramtest.Server, int64) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "ganglion")
	if out, err := exec.Command("go", "build", "-o", bin, "..").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fi, err := os.Stat(bin)
	if err != nil {
		t.Fatal(err)
	}
	fake := telegramtest.NewServer(t, crashToken)
	cmd := exec.Command(bin, "run", "--roles", "core")
	// config may hold an agent section of its own: the workspace goes into
	// it, or into a new one.
	ws := "workspace: \"" + filepath.Join(dir, "workspace") + "\""
	if rest, ok := strings.CutPrefix(config, "agent: {"); ok {
		config = "agent: {" + ws + ", " + rest
	} else {
		config = "agent: {" + ws + "}\n" + config
	}
	writeConfig(t, dir, crashTelegram(fake.URL())+"metrics: {addr: \"127.0.0.1:0\"}\n"+config)
	cmd.Env = childEnv("GANGLION_DB="+filepath.Join(dir, "mem.db"), "FAKE_KEY=sk-measure", "TELEGRAM_BOT_TOKEN="+crashToken)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd, fake, fi.Size()
}

func stopMeasured(cmd *exec.Cmd) {
	_ = cmd.Process.Signal(syscall.SIGTERM)
	_ = cmd.Wait()
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
