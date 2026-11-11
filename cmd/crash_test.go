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
	writeConfig(t, dir, crashTelegram(fake.URL())+"metrics: {addr: \"127.0.0.1:0\"}\n")
	env := childEnv("GANGLION_DB="+filepath.Join(dir, "crash.db"), "TELEGRAM_BOT_TOKEN="+crashToken)

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

// crashTelegram is the telegram section of a child node on the fake Bot API
// at url, with the admin of the tests.
func crashTelegram(url string) string {
	return "telegram: {api_url: \"" + url + "\", admin: \"telegram:user:" + strconv.Itoa(adminID) + "\"}\n"
}

// fakeModels is a models section with one model, m, on an OpenAI-compatible
// fake at url whose key is in the variable keyVar.
func fakeModels(url, keyVar string) string {
	return "models:\n  providers: [{name: fake, type: openai, base_url: \"" + url + "\", key: env:" + keyVar + "}]\n" +
		"  models: [{id: m, provider: fake, name: m, tools: true, context: 64000}]\n  purposes: {chat: [{model: m}]}\n"
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

// crashAddEvery makes every n-th message of the agent harness come with a
// second one soon after, so it may join the running turn (R-LIVE-1).
const crashAddEvery = 3

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
			Args: fmt.Sprintf(`{"topic":"fact %s","summary":"A number","fact":"The owner's number is %s."}`, marker, marker)}
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
	writeConfig(t, dir, crashTelegram(n.fake.URL())+"metrics: {addr: \"127.0.0.1:0\"}\n"+
		"agent: {workspace: \""+n.workspace()+"\", limits: {daily_turns: off, daily_tokens: off}, search: {mode: off}}\n"+
		fakeModels(n.model.URL(), "FAKE_KEY"))
	n.env = childEnv("GANGLION_DB="+filepath.Join(dir, "crash.db"), "FAKE_KEY=sk-crash", "TELEGRAM_BOT_TOKEN="+crashToken)
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
		} else if !strings.HasPrefix(s.Text, "Memory updated") {
			// A stored fact's note with Undo (R-MEM-9) is expected too.
			other = append(other, s.Text)
		}
	}
	return markers, other
}

// TestCrashHarnessAgent kills the agent with SIGKILL at random moments while
// messages arrive (3.8, R-DLV-3 to R-DLV-6). In the end every message has its
// answer, in order, at most one duplicate per kill; every fact was stored by
// exactly one commit, its note sent beside the answer; the journal holds one
// call per message, so a rerun kept
// the call's key; and every /metrics sent its photo. Some messages arrive
// during a turn and join it: such a message counts as answered when the turn
// that took it answered, and its fact is stored at most once, since a rerun may
// take it at another moment and give the call number to another call (ADR-0050).
// Run it with `make crash-agent`; it spends a few minutes.
func TestCrashHarnessAgent(t *testing.T) {
	if os.Getenv("GANGLION_CRASH_AGENT") == "" {
		t.Skip("set GANGLION_CRASH_AGENT=1 (make crash-agent)")
	}
	iters := crashIters(t, 200)
	n := newCrashAgentNode(t)

	var want []string
	var metrics int
	var lastUpdate int64
	send := func(text string) {
		lastUpdate = n.fake.AddMessage(telegramtest.Incoming{ChatID: adminID, UserID: adminID, Name: "Admin", Text: text})
	}
	for i := range iters {
		cmd := n.start(t)
		text := fmt.Sprintf("m%03d", i*2)
		if i%crashMetricsEvery == crashMetricsEvery-1 {
			text = "/metrics"
			metrics++
		} else {
			want = append(want, text)
		}
		send(text)
		if i%crashAddEvery == crashAddEvery-1 {
			time.Sleep(time.Duration(rand.IntN(200)) * time.Millisecond)
			added := fmt.Sprintf("m%03d", i*2+1)
			want = append(want, added)
			send(added)
		}
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
		// The newest message is the last to be answered, whichever turn took it.
		if slices.Contains(got, want[len(want)-1]) && len(n.fake.Uploads()) >= metrics && n.fake.Confirmed() >= lastUpdate {
			break
		}
		if time.Now().After(deadline) {
			t.Errorf("after 3 min: %d answers for %d messages, %d of %d photos, confirmed %d of %d",
				len(slices.Compact(got)), len(want), len(n.fake.Uploads()), metrics, n.fake.Confirmed(), lastUpdate)
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	stop()

	groups, taken := n.turnGroups(t)
	n.checkInbox(t, want, groups)
	n.checkAnswers(t, want, groups, iters)
	n.checkModelSaw(t, groups)
	n.checkPhotos(t, metrics, iters)
	n.checkFacts(t, groups)
	n.checkJournal(t, len(groups), len(want), metrics)
	n.checkHistory(t)
	if iters >= 100 && taken == 0 {
		t.Error("no message joined a running turn: the harness tested nothing")
	}
	turns := 0
	for _, r := range n.model.Requests() {
		if len(r.Tools) > 0 && r.Messages[len(r.Messages)-1]["role"] != "tool" {
			turns++
		}
	}
	t.Logf("%d messages for the model took %d groups, %d joined a turn; %d calls began from a person's message", len(want), len(groups), taken, turns)
	if iters >= 100 && turns == len(groups) {
		t.Error("no kill landed inside a turn: the harness tested nothing")
	}
}

// turnGroups reads which messages each turn answered: a turn's own message
// and those it took (inbox.taken_by), by the marker of each, oldest first. Every
// message of the harness must be finished. taken counts the ones that joined.
func (n *crashAgentNode) turnGroups(t *testing.T) (groups [][]string, taken int) {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), filepath.Join(n.dir, "crash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query(`SELECT id, payload, status, COALESCE(taken_by, '') FROM inbox ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	at := map[string]int{}
	for rows.Next() {
		var id, payload, status, by string
		if err := rows.Scan(&id, &payload, &status, &by); err != nil {
			t.Fatal(err)
		}
		marker := crashMarker.FindString(payload)
		if marker == "" {
			continue // a command
		}
		if status != "done" {
			t.Errorf("message %s is %s", marker, status)
		}
		if by == "" {
			at[id] = len(groups)
			groups = append(groups, []string{marker})
			continue
		}
		i, ok := at[by]
		if !ok {
			t.Errorf("message %s taken by %s, which has no turn", marker, by)
			continue
		}
		groups[i] = append(groups[i], marker)
		taken++
	}
	return groups, taken
}

// checkInbox wants every message of the harness in the inbox, once: one that
// never got there would have no turn to answer it.
func (n *crashAgentNode) checkInbox(t *testing.T, want []string, groups [][]string) {
	t.Helper()
	got := slices.Concat(groups...)
	slices.Sort(got)
	sorted := slices.Clone(want)
	slices.Sort(sorted)
	if !slices.Equal(got, sorted) {
		t.Errorf("the inbox and the messages sent differ:\n got %v\nwant %v", got, sorted)
	}
}

// checkModelSaw wants every message of a turn in one request to the model: a
// message marked as taken that the model never read is a lost message.
func (n *crashAgentNode) checkModelSaw(t *testing.T, groups [][]string) {
	t.Helper()
	requests := n.model.Requests()
	for _, g := range groups {
		if !slices.ContainsFunc(requests, func(r providertest.Request) bool {
			users := userMarkers(r)
			return !slices.ContainsFunc(g, func(m string) bool { return !slices.Contains(users, m) })
		}) {
			t.Errorf("no request to the model holds all of the turn %v", g)
		}
	}
}

// checkAnswers wants the answers in order, and for every turn its reply: it names
// the newest message the turn took, as an earlier answer names an older one.
func (n *crashAgentNode) checkAnswers(t *testing.T, want []string, groups [][]string, kills int) {
	t.Helper()
	got, other := n.answers()
	compact := slices.Compact(slices.Clone(got))
	pos := -1
	for _, m := range compact {
		i := slices.Index(want, m)
		if i <= pos {
			t.Errorf("answer %s out of order:\n got %v\nwant %v", m, compact, want)
			break
		}
		pos = i
	}
	for _, g := range groups {
		if !slices.Contains(compact, g[len(g)-1]) {
			t.Errorf("no reply to the turn of %v", g)
		}
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
// commit for each fact of a message alone in its turn, and at most one for any.
func (n *crashAgentNode) checkFacts(t *testing.T, groups [][]string) {
	t.Helper()
	out, err := exec.Command("git", "-C", n.workspace(), "log", "--format=%s").CombinedOutput()
	if err != nil {
		t.Fatalf("git log: %v\n%s", err, out)
	}
	commits := map[string]int{}
	for line := range strings.SplitSeq(string(out), "\n") {
		if strings.HasPrefix(line, "memory: add ") {
			commits[crashMarker.FindString(line)]++
		}
	}
	for _, g := range groups {
		for _, m := range g {
			if commits[m] > 1 || (len(g) == 1 && commits[m] != 1) {
				t.Errorf("fact %s, in a turn of %d, stored by %d commits", m, len(g), commits[m])
			}
		}
	}
}

// checkJournal counts the calls: a rerun replaces its own row, so a key that
// changed on a rerun shows as a second row.
func (n *crashAgentNode) checkJournal(t *testing.T, minFacts, maxFacts, metrics int) {
	t.Helper()
	db, err := sqlitedb.Open(context.Background(), filepath.Join(n.dir, "crash.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	for tool, want := range map[string][2]int{"memory.write": {minFacts, maxFacts}, "metrics.show": {metrics, metrics}} {
		var rows, failed int
		err := db.QueryRow(`SELECT COUNT(*), COUNT(NULLIF(err, '')) FROM tool_calls WHERE tool = ?`, tool).Scan(&rows, &failed)
		if err != nil {
			t.Fatal(err)
		}
		if rows < want[0] || rows > want[1] || failed != 0 {
			t.Errorf("journal: %d calls of %s (%d failed), want %d to %d", rows, tool, failed, want[0], want[1])
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
	writeConfig(t, dir, crashTelegram(fake.URL())+"metrics: {addr: off}\n")
	cmd.Env = childEnv("GANGLION_DB="+filepath.Join(dir, "env.db"), "TELEGRAM_BOT_TOKEN="+crashToken)
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
	writeConfig(t, dir, crashTelegram(fake.URL())+"metrics: {addr: off}\n")
	cmd.Env = childEnv("GANGLION_DB="+filepath.Join(dir, "kill.db"), "TELEGRAM_BOT_TOKEN="+crashToken)
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
