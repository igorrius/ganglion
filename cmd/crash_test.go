//go:build unix

package cmd

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/igorrius/ganglion/internal/testinputs"
	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

const (
	crashToken    = "7001:crash-secret"
	crashMessages = 200
	adminID       = 42
	groupID       = -100
)

// TestCrashHarness kills the echo bot with SIGKILL at random moments while
// 200 messages arrive, then lets it finish. Every message gets its echo,
// in order per chat, with at most one duplicate per kill (R-DLV-5).
func TestCrashHarness(t *testing.T) {
	if testing.Short() {
		t.Skip("crash harness skipped with -short")
	}
	iters := crashIters(t, 40)

	dir := t.TempDir()
	bin := filepath.Join(dir, "ganglion")
	testinputs.Build(t, "..")
	if out, err := exec.Command("go", "build", "-o", bin, "..").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fake := telegramtest.NewServer(t, crashToken)
	env := append(os.Environ(),
		"GANGLION_DB="+filepath.Join(dir, "crash.db"),
		"TELEGRAM_BOT_TOKEN="+crashToken,
		"TELEGRAM_BOT_TOKEN_FILE=",
		"TELEGRAM_API_URL="+fake.URL(),
		"GANGLION_ADMIN=telegram:user:"+strconv.Itoa(adminID),
		"GANGLION_METRICS_ADDR=127.0.0.1:0",
	)

	// Messages arrive gradually while the process is being killed: one
	// kill cycle takes about 250 ms, so 200 messages spread over iters
	// cycles come every iters×1.25 ms and the last ones land in the last
	// cycles.
	chats := map[int64][]string{}
	var lastUpdate int64
	var wg sync.WaitGroup
	wg.Go(func() {
		for i := range crashMessages {
			chat := int64(adminID)
			if i%2 == 1 {
				chat = groupID
			}
			text := fmt.Sprintf("m%03d", i)
			chats[chat] = append(chats[chat], text)
			lastUpdate = fake.AddMessage(telegramtest.Incoming{ChatID: chat, UserID: adminID, Name: "Admin", Text: text})
			time.Sleep(time.Duration(iters) * 1250 * time.Microsecond)
		}
	})

	var lastLog bytes.Buffer
	start := func() *exec.Cmd {
		lastLog.Reset()
		cmd := exec.Command(bin, "run", "--roles", "core")
		cmd.Env = env
		cmd.Stdout, cmd.Stderr = &lastLog, &lastLog
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	for range iters {
		cmd := start()
		time.Sleep(time.Duration(rand.IntN(400)) * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
	}
	wg.Wait()
	beforeFinal := len(fake.Sent())

	cmd := start()
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("last process output:\n%s", lastLog.String())
			logStuckLeases(t, filepath.Join(dir, "crash.db"))
		}
	}()

	replies := func() map[int64][]string {
		out := map[int64][]string{}
		for _, s := range fake.Sent() {
			out[s.ChatID] = append(out[s.ChatID], strings.TrimPrefix(s.Text, "echo: "))
		}
		return out
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		got := replies()
		if len(slices.Compact(slices.Clone(got[adminID])))+len(slices.Compact(slices.Clone(got[groupID]))) >= crashMessages &&
			fake.Confirmed() >= lastUpdate {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("after 60 s: %d+%d replies, confirmed %d of %d",
				len(got[adminID]), len(got[groupID]), fake.Confirmed(), lastUpdate)
		}
		time.Sleep(50 * time.Millisecond)
	}

	got := replies()
	dups := 0
	for chat, want := range chats {
		seq := got[chat]
		compact := slices.Compact(slices.Clone(seq))
		dups += len(seq) - len(compact)
		if !slices.Equal(compact, want) {
			t.Errorf("chat %d: replies out of order or missing:\n got %v\nwant %v", chat, compact, want)
		}
	}
	if extra := len(got) - len(chats); extra != 0 {
		t.Errorf("replies to %d unexpected chats", extra)
	}
	if dups > iters {
		t.Errorf("%d duplicate replies after %d kills, want at most %d", dups, iters, iters)
	}
	t.Logf("%d kills, %d replies sent before the final run, %d duplicate replies, confirmed up to %d",
		iters, beforeFinal, dups, fake.Confirmed())
}

// crashIters is the number of kills: def, or GANGLION_CRASH_ITERS.
func crashIters(t *testing.T, def int) int {
	t.Helper()
	v := os.Getenv("GANGLION_CRASH_ITERS")
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		t.Fatalf("GANGLION_CRASH_ITERS=%q", v)
	}
	return n
}

// logStuckLeases reports pending inbox events still under a lease: a
// conversation waits behind them until the lease runs out.
func logStuckLeases(t *testing.T, path string) {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), path)
	if err != nil {
		t.Logf("open %s: %v", path, err)
		return
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT conv, idem_key, attempts, lease_until_ms FROM inbox
		WHERE status = 'pending' AND lease_until_ms > ? ORDER BY seq`, time.Now().UnixMilli())
	if err != nil {
		t.Logf("query inbox: %v", err)
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var conv, key string
		var attempts, until int64
		if err := rows.Scan(&conv, &key, &attempts, &until); err == nil {
			t.Logf("stuck lease: conv %s event %s attempts %d until %s", conv, key, attempts,
				time.UnixMilli(until).Format(time.TimeOnly))
		}
	}
}

// crashMarker finds a harness message in a text.
var crashMarker = regexp.MustCompile(`\bm\d{3,}\b`)

// crashMetricsEvery makes every n-th message of the agent harness a
// /metrics.
const crashMetricsEvery = 10

// crashModel is a fake model whose every turn has one real side effect: it
// writes a fact named after the person's message (a file and a git commit),
// reads the result and answers with the same marker.
func crashModel(t *testing.T) *providertest.Server {
	t.Helper()
	model := providertest.NewServer(t, "sk-crash")
	model.Respond(func(r providertest.Request) providertest.Answer {
		// A model takes time: most kills should land inside a turn.
		time.Sleep(time.Duration(rand.IntN(60)) * time.Millisecond)
		if len(r.Tools) == 0 { // the memory's clean-context check
			return providertest.Answer{Text: `{"ok":true}`}
		}
		var marker string
		if users := userMarkers(r); len(users) > 0 {
			marker = users[len(users)-1]
		}
		if r.Messages[len(r.Messages)-1]["role"] == "tool" {
			return providertest.Answer{Text: "answer " + marker}
		}
		return providertest.Answer{Tool: "memory.write",
			Args: fmt.Sprintf(`{"title":"fact %s","body":"The owner's number is %s."}`, marker, marker)}
	})
	return model
}

// userMarkers lists the harness messages among the person's messages of a
// request, oldest first.
func userMarkers(r providertest.Request) []string {
	var out []string
	for _, m := range r.Messages {
		if m["role"] == "user" {
			out = append(out, crashMarker.FindAllString(fmt.Sprint(m["content"]), -1)...)
		}
	}
	return out
}

// crashAgentNode runs the agent's binary on a fake Bot API and a fake model.
type crashAgentNode struct {
	bin, dir string
	env      []string
	fake     *telegramtest.Server
	model    *providertest.Server
	log      *lockedBuffer // of the last run
}

func newCrashAgentNode(t *testing.T) *crashAgentNode {
	t.Helper()
	dir := t.TempDir()
	n := &crashAgentNode{bin: filepath.Join(dir, "ganglion"), dir: dir,
		fake: telegramtest.NewServer(t, crashToken), model: crashModel(t)}
	testinputs.Build(t, "..")
	if out, err := exec.Command("go", "build", "-o", n.bin, "..").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	catalog := filepath.Join(dir, "models.yaml")
	yaml := "providers:\n  - {name: fake, type: openai, base_url: " + n.model.URL() + ", key_env: FAKE_KEY}\n" +
		"models:\n  - {provider: fake, name: m, tier: smart, tools: true, context: 64000}\n"
	if err := os.WriteFile(catalog, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	n.env = append(os.Environ(),
		"GANGLION_DB="+filepath.Join(dir, "crash.db"),
		"GANGLION_WORKSPACE="+n.workspace(),
		"GANGLION_MODELS_FILE="+catalog, "FAKE_KEY=sk-crash",
		"GANGLION_DAILY_TURNS=0", "GANGLION_DAILY_TOKENS=0", "GANGLION_SEARCH=off",
		"TELEGRAM_BOT_TOKEN="+crashToken, "TELEGRAM_BOT_TOKEN_FILE=", "TELEGRAM_API_URL="+n.fake.URL(),
		"GANGLION_ADMIN=telegram:user:"+strconv.Itoa(adminID), "GANGLION_METRICS_ADDR=127.0.0.1:0",
	)
	return n
}

func (n *crashAgentNode) workspace() string { return filepath.Join(n.dir, "workspace") }

func (n *crashAgentNode) start(t *testing.T) *exec.Cmd {
	t.Helper()
	n.log = &lockedBuffer{}
	cmd := exec.Command(n.bin, "run", "--roles", "core")
	cmd.Env = n.env
	cmd.Stdout, cmd.Stderr = n.log, n.log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

// answers lists the markers of the model's answers as sent, and any other
// text the node sent.
func (n *crashAgentNode) answers() (markers, other []string) {
	for _, s := range n.fake.Sent() {
		if m, ok := strings.CutPrefix(s.Text, "answer "); ok {
			markers = append(markers, m)
		} else {
			other = append(other, s.Text)
		}
	}
	return markers, other
}

// TestCrashHarnessAgent kills the agent with SIGKILL at random moments while
// messages arrive (3.8, R-DLV-3 to R-DLV-6). In the end every message has its
// answer, in order, at most one duplicate per kill; every fact was stored by
// exactly one commit; the journal holds one call per message, so a rerun kept
// the call's key; and every /metrics sent its photo. Run it with
// `make crash-agent`; it spends a few minutes.
func TestCrashHarnessAgent(t *testing.T) {
	if os.Getenv("GANGLION_CRASH_AGENT") == "" {
		t.Skip("set GANGLION_CRASH_AGENT=1 (make crash-agent)")
	}
	iters := crashIters(t, 200)
	n := newCrashAgentNode(t)

	var want []string
	var metrics int
	var lastUpdate int64
	for i := range iters {
		cmd := n.start(t)
		text := fmt.Sprintf("m%03d", i)
		if i%crashMetricsEvery == crashMetricsEvery-1 {
			text = "/metrics"
			metrics++
		} else {
			want = append(want, text)
		}
		lastUpdate = n.fake.AddMessage(telegramtest.Incoming{ChatID: adminID, UserID: adminID, Name: "Admin", Text: text})
		time.Sleep(time.Duration(rand.IntN(600)) * time.Millisecond)
		_ = cmd.Process.Signal(syscall.SIGKILL)
		_ = cmd.Wait()
	}

	cmd := n.start(t)
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			_ = cmd.Process.Signal(syscall.SIGTERM)
			_ = cmd.Wait()
		}
	}
	defer func() {
		stop()
		if t.Failed() {
			t.Logf("last process output:\n%s", n.log.String())
			logStuckLeases(t, filepath.Join(n.dir, "crash.db"))
		}
	}()
	deadline := time.Now().Add(3 * time.Minute)
	for {
		got, _ := n.answers()
		if len(slices.Compact(got)) >= len(want) && len(n.fake.Uploads()) >= metrics && n.fake.Confirmed() >= lastUpdate {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("after 3 min: %d of %d answers, %d of %d photos, confirmed %d of %d",
				len(slices.Compact(got)), len(want), len(n.fake.Uploads()), metrics, n.fake.Confirmed(), lastUpdate)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()

	n.checkAnswers(t, want, iters)
	n.checkPhotos(t, metrics, iters)
	n.checkFacts(t, want)
	n.checkJournal(t, len(want), metrics)
	n.checkHistory(t)
	turns := 0
	for _, r := range n.model.Requests() {
		if len(r.Tools) > 0 && r.Messages[len(r.Messages)-1]["role"] != "tool" {
			turns++
		}
	}
	t.Logf("%d messages for the model took %d turns: %d reruns", len(want), turns, turns-len(want))
	if iters >= 100 && turns == len(want) {
		t.Error("no kill landed inside a turn: the harness tested nothing")
	}
}

func (n *crashAgentNode) checkAnswers(t *testing.T, want []string, kills int) {
	t.Helper()
	got, other := n.answers()
	compact := slices.Compact(slices.Clone(got))
	if !slices.Equal(compact, want) {
		t.Errorf("answers out of order or missing:\n got %v\nwant %v", compact, want)
	}
	if dups := len(got) - len(compact); dups > kills {
		t.Errorf("%d duplicate answers after %d kills", dups, kills)
	} else {
		t.Logf("%d kills, %d answers, %d duplicates", kills, len(got), dups)
	}
	if len(other) > 0 {
		t.Errorf("%d messages that are no answer, first: %q", len(other), other[0])
	}
}

func (n *crashAgentNode) checkPhotos(t *testing.T, metrics, kills int) {
	t.Helper()
	ups := n.fake.Uploads()
	if len(ups) < metrics || len(ups) > metrics+kills {
		t.Errorf("%d photos for %d /metrics after %d kills", len(ups), metrics, kills)
	}
	for _, u := range ups {
		if u.Method != "sendPhoto" || len(u.Data) == 0 {
			t.Errorf("upload %s with %d bytes", u.Method, len(u.Data))
		}
	}
}

// checkFacts reads the side effect itself: the memory's git history has one
// commit for each fact.
func (n *crashAgentNode) checkFacts(t *testing.T, want []string) {
	t.Helper()
	out, err := exec.Command("git", "-C", n.workspace(), "log", "--format=%s").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	commits := map[string]int{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "memory: remember ") {
			commits[crashMarker.FindString(line)]++
		}
	}
	for _, m := range want {
		if commits[m] != 1 {
			t.Errorf("fact %s stored by %d commits", m, commits[m])
		}
	}
}

// checkJournal counts the calls: a rerun replaces its own row, so a key that
// changed on a rerun shows as a second row.
func (n *crashAgentNode) checkJournal(t *testing.T, facts, metrics int) {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), filepath.Join(n.dir, "crash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for tool, want := range map[string]int{"memory.write": facts, "metrics.show": metrics} {
		var rows, failed int
		err := db.QueryRow(`SELECT COUNT(*), COUNT(NULLIF(err, '')) FROM tool_calls WHERE tool = ?`, tool).Scan(&rows, &failed)
		if err != nil {
			t.Fatal(err)
		}
		if rows != want || failed != 0 {
			t.Errorf("journal: %d calls of %s (%d failed), want %d", rows, tool, failed, want)
		}
	}
}

// checkHistory reads the last conversation the model saw: a rerun must not
// add a person's message twice.
func (n *crashAgentNode) checkHistory(t *testing.T) {
	t.Helper()
	for _, r := range slices.Backward(n.model.Requests()) {
		if len(r.Tools) == 0 {
			continue
		}
		users := userMarkers(r)
		slices.Sort(users)
		if len(slices.Compact(slices.Clone(users))) != len(users) {
			t.Errorf("a message twice in the history: %v", users)
		}
		return
	}
	t.Error("the model saw no turn")
}

// Final review: a script runs as the node's user, so without this it could
// read the node's token from /proc/<node>/environ, whatever its own
// environment holds.
func TestNodeEnvironmentIsNotReadable(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("no /proc")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ganglion")
	testinputs.Build(t, "..")
	if out, err := exec.Command("go", "build", "-o", bin, "..").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fake := telegramtest.NewServer(t, crashToken)
	cmd := exec.Command(bin, "run", "--roles", "core")
	cmd.Env = append(os.Environ(), "GANGLION_DB="+filepath.Join(dir, "env.db"), "TELEGRAM_BOT_TOKEN="+crashToken, "TELEGRAM_BOT_TOKEN_FILE=",
		"TELEGRAM_API_URL="+fake.URL(), "GANGLION_ADMIN=telegram:user:"+strconv.Itoa(adminID), "GANGLION_METRICS_ADDR=off", "GANGLION_MODELS_FILE=")
	var stdout lockedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		_ = cmd.Wait()
	}()
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(stdout.String(), "node started") {
		if time.Now().After(deadline) {
			t.Fatalf("the node did not start:\n%s", stdout.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", cmd.Process.Pid))
	if err == nil {
		t.Fatalf("another process of the same user read the node's environment (%d bytes)", len(b))
	}
}

// R-OBS-6: the last line the node logged before a kill -9 is on disk.
// The disk handler runs before stdout's, so a line seen on stdout is
// already in the file.
func TestLastLineSurvivesKill(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the binary")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "ganglion")
	testinputs.Build(t, "..")
	if out, err := exec.Command("go", "build", "-o", bin, "..").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	fake := telegramtest.NewServer(t, crashToken)
	cmd := exec.Command(bin, "run", "--roles", "core")
	cmd.Env = append(os.Environ(), "GANGLION_DB="+filepath.Join(dir, "kill.db"), "GANGLION_LOG_DIR=",
		"TELEGRAM_BOT_TOKEN="+crashToken, "TELEGRAM_BOT_TOKEN_FILE=", "TELEGRAM_API_URL="+fake.URL(),
		"GANGLION_ADMIN=telegram:user:"+strconv.Itoa(adminID), "GANGLION_METRICS_ADDR=off", "GANGLION_MODELS_FILE=")
	var stdout lockedBuffer
	cmd.Stdout, cmd.Stderr = &stdout, &stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	fake.AddMessage(telegramtest.Incoming{ChatID: adminID, UserID: adminID, Name: "Admin", Text: "marker-7731"})
	deadline := time.Now().Add(20 * time.Second)
	for !strings.Contains(stdout.String(), "outbox: sent") {
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("no send logged:\n%s", stdout.String())
		}
		time.Sleep(time.Millisecond)
	}
	_ = cmd.Process.Signal(syscall.SIGKILL)
	_ = cmd.Wait()
	b, err := os.ReadFile(filepath.Join(dir, "logs", "ganglion.log"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"msg":"outbox: sent"`) {
		t.Fatalf("the last line before the kill is not on disk:\n%s", b)
	}
}
