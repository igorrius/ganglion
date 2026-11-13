package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/igorrius/ganglion/provider/providertest"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

const (
	familyID    = -1001 // a forum group nobody connected
	familyTopic = 45
	annID       = 9 // a member of the group who is not on the node's list
)

// waitFor polls cond for up to 20 s.
func waitFor(t *testing.T, n *crashAgentNode, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if cond() {
			return
		}
	}
	t.Fatalf("timed out waiting for %s; sent %+v\nlog:\n%s", what, n.fake.Sent(), n.log.String())
}

// sentTo lists what the node sent to chat.
func sentTo(n *crashAgentNode, chat int64) []telegramtest.Outgoing {
	var out []telegramtest.Outgoing
	for _, s := range n.fake.Sent() {
		if s.ChatID == chat {
			out = append(out, s)
		}
	}
	return out
}

// callbackOf is the callback_data of the button at index i of a keyboard.
func callbackOf(t *testing.T, keyboard string, i int) string {
	t.Helper()
	var rows [][]struct {
		Text string `json:"text"`
		Data string `json:"callback_data"`
	}
	if err := json.Unmarshal([]byte(keyboard), &rows); err != nil || len(rows) <= i || len(rows[i]) != 1 {
		t.Fatalf("keyboard %q: %v", keyboard, err)
	}
	return rows[i][0].Data
}

// TestConnectAGroupThroughTheCard runs the node's binary against a fake Bot
// API and a fake model (R-DLV-16, R-DLV-17, R-POL-10): a group nobody
// connected gets one notice and the admin one card; Allow writes the
// group's line to ganglion.yaml and the group works at once, for a member
// who is not on the node's list, and only when the bot is addressed; the
// admin has no tools there.
func TestConnectAGroupThroughTheCard(t *testing.T) {
	if testing.Short() {
		t.Skip("builds and runs the node; skipped with -short")
	}
	n := newCrashAgentNode(t)
	// The model says what it was offered, so the test sees the role.
	n.model.Respond(func(r providertest.Request) providertest.Answer {
		if len(r.Tools) == 0 {
			return providertest.Answer{Text: "answer without tools"}
		}
		return providertest.Answer{Text: "answer with tools"}
	})
	cmd := n.start(t)
	t.Cleanup(func() { _ = cmd.Process.Signal(syscall.SIGKILL); _ = cmd.Wait() })

	ask := func(user int64, name, text string) {
		n.fake.AddMessage(telegramtest.Incoming{ChatID: familyID, ThreadID: familyTopic, Title: "Family", UserID: user, Name: name,
			Text: text, Extra: telegramtest.Mention(text)})
	}

	// 1. A stranger addresses the bot in a group that is not connected.
	ask(annID, "Ann", "@test_bot hello")
	waitFor(t, n, "the notice and the card", func() bool { return len(sentTo(n, familyID)) == 1 && len(sentTo(n, adminID)) == 1 })
	notice, card := sentTo(n, familyID)[0], sentTo(n, adminID)[0]
	if notice.ThreadID != familyTopic || !strings.Contains(notice.Text, "no permission") || notice.Keyboard != "" {
		t.Fatalf("notice %+v", notice)
	}
	if !strings.Contains(card.Text, "Family") || !strings.Contains(card.Text, "telegram:-1001") || !strings.Contains(card.Text, "Ann") {
		t.Fatalf("card %+v", card)
	}
	if got := len(n.model.Requests()); got != 0 {
		t.Fatalf("%d model calls before the group is connected", got)
	}

	// 2. A second message asks nobody again.
	ask(annID, "Ann", "@test_bot hello?")
	last := n.fake.AddMessage(telegramtest.Incoming{ChatID: familyID, ThreadID: familyTopic, UserID: annID, Text: "anyone?"})
	waitFor(t, n, "the updates to be confirmed", func() bool { return n.fake.Confirmed() >= last })
	if len(sentTo(n, familyID)) != 1 || len(sentTo(n, adminID)) != 1 || len(n.model.Requests()) != 0 {
		t.Fatalf("a second request went out: %+v", n.fake.Sent())
	}

	// 3. Abuse: the stranger cannot press the admin's button, even were it shown to them.
	allow := callbackOf(t, card.Keyboard, 0)
	n.fake.AddCallback(telegramtest.Callback{ChatID: adminID, UserID: annID, MessageID: card.MessageID, Data: allow})
	// 4. The admin presses Allow: the card changes and the file has the line.
	n.fake.AddCallback(telegramtest.Callback{ChatID: adminID, UserID: adminID, Name: "Admin", MessageID: card.MessageID, Data: allow})
	file := settings.TelegramFile{Path: filepath.Join(n.dir, settings.FileName)}
	waitFor(t, n, "the group's line", func() bool {
		gs, err := file.Groups()
		return err == nil && len(gs) == 2
	})
	gs, _ := file.Groups()
	if gs[1] != (settings.Group{Chat: familyID, Name: "Family", Role: settings.GroupRoleFamily}) {
		t.Fatalf("groups %+v", gs)
	}
	waitFor(t, n, "the card to say connected", func() bool {
		for _, e := range n.fake.Edits() {
			if e.ChatID == adminID && e.MessageID == card.MessageID && strings.Contains(e.Text, "is connected") && e.Keyboard == "" {
				return true
			}
		}
		return false
	})
	raw, err := os.ReadFile(file.Path)
	if err != nil || !strings.Contains(string(raw), "api_url") {
		t.Fatalf("the file lost its other settings: %v\n%s", err, raw)
	}

	// 5. With no restart the stranger is answered, in the topic, with no tools.
	ask(annID, "Ann", "@test_bot hello again")
	waitFor(t, n, "an answer in the topic", func() bool { return len(sentTo(n, familyID)) == 2 })
	if got := sentTo(n, familyID)[1]; got.ThreadID != familyTopic || got.Text != "answer without tools" {
		t.Fatalf("answer %+v", got)
	}

	// 6. What does not address the bot gets no answer and no model call.
	calls := len(n.model.Requests())
	last = n.fake.AddMessage(telegramtest.Incoming{ChatID: familyID, ThreadID: familyTopic, UserID: annID, Text: "nice weather"})
	n.fake.AddMessage(telegramtest.Incoming{ChatID: adminID, UserID: adminID, Name: "Admin", Text: "private"})
	waitFor(t, n, "the admin's private answer", func() bool { return len(sentTo(n, adminID)) >= 2 && n.fake.Confirmed() >= last })
	if got := sentTo(n, adminID); got[len(got)-1].Text != "answer with tools" {
		t.Fatalf("the admin's private chat lost its tools: %+v", got)
	}
	if len(sentTo(n, familyID)) != 2 || len(n.model.Requests()) != calls+1 {
		t.Fatalf("an unaddressed message was answered: %+v", sentTo(n, familyID))
	}

	// 7. The admin in the group is offered no tools either (R-POL-10).
	ask(adminID, "Admin", "@test_bot status please")
	waitFor(t, n, "the admin's answer in the group", func() bool { return len(sentTo(n, familyID)) == 3 })
	if got := sentTo(n, familyID)[2].Text; got != "answer without tools" {
		t.Fatalf("the admin's turn in a group: %q", got)
	}
}
