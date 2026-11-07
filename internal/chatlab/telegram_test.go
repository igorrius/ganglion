package chatlab

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

const testBot = "7001:lab-secret"

// botSays has the bot send text with buttons, as the node's adapter would,
// and returns the message's id.
func botSays(t *testing.T, fake *telegramtest.Server, text string, buttons ...string) int64 {
	t.Helper()
	body := map[string]any{"chat_id": 42, "text": text}
	if len(buttons) > 0 {
		var row []map[string]string
		for _, b := range buttons {
			row = append(row, map[string]string{"text": b, "callback_data": "data-" + b + "-" + text})
		}
		body["reply_markup"] = map[string]any{"inline_keyboard": [][]map[string]string{row}}
	}
	postBot(t, fake, "sendMessage", body)
	return fake.LastMessageID()
}

func newTestPeer(t *testing.T) (*Telegram, *telegramtest.Server) {
	t.Helper()
	fake := telegramtest.NewServer(t, testBot)
	return NewTelegram(fake, 42, "Igor"), fake
}

func TestSayAddsAMessage(t *testing.T) {
	p, fake := newTestPeer(t)
	if err := p.Say(context.Background(), "привет"); err != nil {
		t.Fatal(err)
	}
	u := pendingUpdates(t, fake)
	if len(u) != 1 || u[0].Message.Text != "привет" || u[0].Message.Chat.ID != 42 || u[0].Message.From.ID != 42 {
		t.Fatalf("updates %+v", u)
	}
}

func TestSendFileDocument(t *testing.T) {
	p, fake := newTestPeer(t)
	err := p.SendFile(context.Background(), File{Kind: "document", Name: "invoice.pdf", Type: "application/pdf", Size: 1200, Caption: "what is this?"})
	if err != nil {
		t.Fatal(err)
	}
	u := pendingUpdates(t, fake)
	d := u[0].Message.Document
	if d == nil || d.FileName != "invoice.pdf" || d.MimeType != "application/pdf" || d.FileSize != 1200 || u[0].Message.Caption != "what is this?" {
		t.Fatalf("update %+v", u[0].Message)
	}
}

func TestSendFilePhoto(t *testing.T) {
	p, fake := newTestPeer(t)
	if err := p.SendFile(context.Background(), File{Kind: "photo", Name: "cat.jpg", Size: 900, Caption: "whose?"}); err != nil {
		t.Fatal(err)
	}
	u := pendingUpdates(t, fake)
	if len(u[0].Message.Photo) == 0 || u[0].Message.Photo[0].FileSize != 900 || u[0].Message.Caption != "whose?" {
		t.Fatalf("update %+v", u[0].Message)
	}
}

func TestSendFileUnknownKind(t *testing.T) {
	p, _ := newTestPeer(t)
	if err := p.SendFile(context.Background(), File{Kind: "voice"}); err == nil {
		t.Fatal("a voice file was accepted")
	}
}

func TestPressFindsTheNewestLabel(t *testing.T) {
	p, fake := newTestPeer(t)
	botSays(t, fake, "first", "Approve")
	newer := botSays(t, fake, "second", "Approve", "Deny")
	if err := p.Press(context.Background(), "Approve"); err != nil {
		t.Fatal(err)
	}
	u := pendingUpdates(t, fake)
	cq := u[0].CallbackQuery
	if cq == nil || cq.Message.MessageID != newer || cq.Data != "data-Approve-second" || cq.From.ID != 42 {
		t.Fatalf("callback %+v", cq)
	}
}

func TestPressMissingLabel(t *testing.T) {
	p, fake := newTestPeer(t)
	botSays(t, fake, "first", "Approve")
	if err := p.Press(context.Background(), "Deny"); !errors.Is(err, ErrNoButton) {
		t.Fatalf("err %v", err)
	}
}

func TestReplyAnswersTheNewestMatch(t *testing.T) {
	p, fake := newTestPeer(t)
	botSays(t, fake, "Clean answer about apples.")
	newer := botSays(t, fake, "More on apples.")
	botSays(t, fake, "About pears.")
	if err := p.Reply(context.Background(), "apples", "Tell me more."); err != nil {
		t.Fatal(err)
	}
	u := pendingUpdates(t, fake)
	r := u[0].Message.ReplyTo
	if len(u) != 1 || u[0].Message.Text != "Tell me more." || r == nil || r.MessageID != newer || r.Text != "More on apples." || !r.From.IsBot {
		t.Fatalf("update %+v, replying to %+v", u, r)
	}
	if p.Taken() {
		t.Fatal("the reply is not waiting for the node")
	}
}

func TestReplyToNothing(t *testing.T) {
	p, fake := newTestPeer(t)
	botSays(t, fake, "About pears.")
	if err := p.Reply(context.Background(), "apples", "Tell me more."); !errors.Is(err, ErrNoMessage) {
		t.Fatalf("err %v", err)
	}
}

func TestMessagesAppliesEdits(t *testing.T) {
	p, fake := newTestPeer(t)
	id := botSays(t, fake, "Create?", "Approve")
	botSays(t, fake, "later")
	postBot(t, fake, "editMessageText", map[string]any{"chat_id": 42, "message_id": id, "text": "Create?\n\nExpired."})
	ms := p.Messages()
	if len(ms) != 2 || ms[0].Text != "Create?\n\nExpired." || !ms[0].Replaced || len(ms[0].Buttons) != 0 {
		t.Fatalf("messages %+v", ms)
	}
	if ms[1].Text != "later" || ms[1].Replaced {
		t.Fatalf("the other message %+v", ms[1])
	}
}

func TestMessagesListsButtons(t *testing.T) {
	p, fake := newTestPeer(t)
	botSays(t, fake, "Create?", "Approve", "Deny")
	if got := p.Messages()[0].Buttons; !slices.Equal(got, []string{"Approve", "Deny"}) {
		t.Fatalf("buttons %v", got)
	}
}

func TestMessagesHoldsUploads(t *testing.T) {
	p, fake := newTestPeer(t)
	botSays(t, fake, "before")
	postPhoto(t, fake, "the dashboard", []byte("png-bytes"))
	botSays(t, fake, "after")
	ms := p.Messages()
	if len(ms) != 3 || ms[1].Text != "the dashboard" || len(ms[1].Files) != 1 {
		t.Fatalf("messages %+v", ms)
	}
	if f := ms[1].Files[0]; f.Kind != "photo" || f.Size != len("png-bytes") {
		t.Fatalf("file %+v", f)
	}
}

func TestTakenFollowsTheConfirmedOffset(t *testing.T) {
	p, fake := newTestPeer(t)
	if !p.Taken() {
		t.Fatal("nothing was said, so nothing is waiting")
	}
	if err := p.Say(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
	if p.Taken() {
		t.Fatal("taken before the node confirmed it")
	}
	confirmAll(t, fake)
	if !p.Taken() {
		t.Fatal("not taken after the node confirmed it")
	}
}

func TestCheckLoopback(t *testing.T) {
	for _, ok := range []string{"http://127.0.0.1:1", "http://[::1]:1"} {
		if err := CheckLoopback(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"https://api.telegram.org", "http://localhost.evil.net", "http://10.0.0.1:1", "::"} {
		if err := CheckLoopback(bad); err == nil {
			t.Errorf("%s was accepted", bad)
		}
	}
}

// update is what the tests read of a pending Bot API update.
type update struct {
	UpdateID int64 `json:"update_id"`
	Message  struct {
		Text, Caption string
		Chat          struct{ ID int64 }
		From          struct{ ID int64 }
		ReplyTo       *struct {
			MessageID int64 `json:"message_id"`
			Text      string
			From      struct {
				IsBot bool `json:"is_bot"`
			}
		} `json:"reply_to_message"`
		Document *struct {
			FileName string `json:"file_name"`
			MimeType string `json:"mime_type"`
			FileSize int64  `json:"file_size"`
		}
		Photo []struct {
			FileSize int64 `json:"file_size"`
		}
	}
	CallbackQuery *struct {
		Data    string
		From    struct{ ID int64 }
		Message struct {
			MessageID int64 `json:"message_id"`
		}
	} `json:"callback_query"`
}

func pendingUpdates(t *testing.T, fake *telegramtest.Server) []update {
	t.Helper()
	var out []update
	if err := json.Unmarshal(postBot(t, fake, "getUpdates", map[string]any{"timeout": 0}), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func confirmAll(t *testing.T, fake *telegramtest.Server) {
	t.Helper()
	u := pendingUpdates(t, fake)
	postBot(t, fake, "getUpdates", map[string]any{"timeout": 0, "offset": u[len(u)-1].UpdateID + 1})
}
