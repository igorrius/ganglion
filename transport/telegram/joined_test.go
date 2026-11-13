package telegram

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

// The bot added to a group becomes a group.joined event in the group's
// conversation, naming who added it (R-DLV-17).
func TestBotAddedToAGroupIsAnEvent(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	fake.AddChatMember(-1001, "Family", 42, "member")
	eventually(t, "an event", func() bool { return len(c.got()) == 1 })
	e := c.got()[0]
	if e.Kind != event.KindGroupJoined || e.Conv != "telegram:-1001" || e.Source != Name {
		t.Fatalf("event %+v", e)
	}
	var g event.GroupSeen
	if err := json.Unmarshal(e.Payload, &g); err != nil {
		t.Fatal(err)
	}
	if g != (event.GroupSeen{Group: "telegram:-1001", Conv: "telegram:-1001", Title: "Family", From: "telegram:user:42", Name: "User"}) {
		t.Fatalf("payload %+v", g)
	}
}

// Leaving and being removed are no events; their updates are confirmed.
func TestLeavingAGroupIsSkipped(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	fake.AddChatMember(-1001, "Family", 42, "left")
	last := fake.AddChatMember(-1001, "Family", 42, "kicked")
	fake.AddMessage(telegramtest.Incoming{ChatID: 1, UserID: 1, Text: "private"})
	eventually(t, "the private message", func() bool { return len(c.got()) == 1 })
	if c.got()[0].Kind != event.KindMessage {
		t.Fatalf("event %+v", c.got()[0])
	}
	eventually(t, "the skipped updates confirmed", func() bool { return fake.Confirmed() >= last })
}

// Polling asks for the bot's own membership changes.
func TestPollingAsksForMyChatMember(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	c := &collector{}
	run(t, newTransport(t, fake), c.sink)
	eventually(t, "a poll", func() bool { return slices.Contains(fake.AllowedUpdates(), "my_chat_member") })
}
