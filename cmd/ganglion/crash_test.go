//go:build unix

package main

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

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
	iters := 40
	if v := os.Getenv("GANGLION_CRASH_ITERS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			t.Fatalf("GANGLION_CRASH_ITERS=%q", v)
		}
		iters = n
	}

	dir := t.TempDir()
	bin := filepath.Join(dir, "ganglion")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
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
		cmd := exec.Command(bin, "--roles", "core")
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
