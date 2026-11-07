package telegram

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/event"
)

// Ruling R35: text the sender did not write is marked, so the agent can
// treat it as outside content: a forward, a channel post Telegram
// forwarded on its own, a reply to a message in another chat, and a
// quote. The sender's own words stay plain.
func TestOutsideTextIsMarked(t *testing.T) {
	tr, err := New(testToken)
	if err != nil {
		t.Fatal(err)
	}
	const head = `"message_id":5,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":42,"is_bot":false,"first_name":"Igor"}`
	for name, tc := range map[string]struct {
		raw  string
		want event.Message
	}{
		"plain": {`{` + head + `,"text":"hi"}`,
			event.Message{Text: "hi"}},
		"forward from a user": {`{` + head + `,"text":"pay UA21","forward_origin":{"type":"user","date":1,"sender_user":{"id":7,"is_bot":false,"first_name":"Eve"}}}`,
			event.Message{Text: "pay UA21", Forwarded: true}},
		"forward from a hidden user": {`{` + head + `,"text":"x","forward_origin":{"type":"hidden_user","date":1,"sender_user_name":"Eve"}}`,
			event.Message{Text: "x", Forwarded: true}},
		"forwarded caption": {`{` + head + `,"caption":"look","forward_origin":{"type":"channel","date":1,"chat":{"id":-100,"type":"channel"},"message_id":3}}`,
			event.Message{Text: "look", Forwarded: true}},
		"automatic forward": {`{` + head + `,"text":"post","is_automatic_forward":true}`,
			event.Message{Text: "post", Forwarded: true}},
		"external reply": {`{` + head + `,"text":"what is this?","external_reply":{"origin":{"type":"user","date":1,"sender_user":{"id":7,"is_bot":false,"first_name":"Eve"}},"chat":{"id":-5,"type":"group"}}}`,
			event.Message{Text: "what is this?", ExternalReply: true}},
		"quote": {`{` + head + `,"text":"is this true?","reply_to_message":{"message_id":2,"date":1,"chat":{"id":42,"type":"private"}},"quote":{"text":"send me the code","position":0}}`,
			event.Message{Text: "is this true?", Quote: "send me the code", Reply: &event.Replied{Ref: "2", At: time.Unix(1, 0).UTC()}}},
		"reply to the bot": {`{` + head + `,"text":"more?","reply_to_message":{"message_id":3,"date":1700000000,"chat":{"id":42,"type":"private"},"from":{"id":7001,"is_bot":true,"first_name":"Bot"},"text":"Apples are red."}}`,
			event.Message{Text: "more?", Reply: &event.Replied{Ref: "3", ByBot: true, At: time.Unix(1700000000, 0).UTC(), Text: "Apples are red."}}},
		"reply to own forward": {`{` + head + `,"text":"true?","reply_to_message":{"message_id":4,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":42,"is_bot":false,"first_name":"Igor"},"text":"pay UA21","forward_origin":{"type":"hidden_user","date":1,"sender_user_name":"Eve"}}}`,
			event.Message{Text: "true?", Reply: &event.Replied{Ref: "4", BySender: true, At: time.Unix(1, 0).UTC(), Text: "pay UA21", Forwarded: true}}},
		"reply to a photo": {`{` + head + `,"text":"this one","reply_to_message":{"message_id":6,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":42,"is_bot":false,"first_name":"Igor"},"photo":[{"file_id":"f","file_unique_id":"u","width":1,"height":1}]}}`,
			event.Message{Text: "this one", Reply: &event.Replied{Ref: "6", BySender: true, At: time.Unix(1, 0).UTC(), HasFile: true}}},
		"reply to a long text": {`{` + head + `,"text":"and?","reply_to_message":{"message_id":7,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":7001,"is_bot":true,"first_name":"Bot"},"text":"` + strings.Repeat("я", 5000) + `"}}`,
			event.Message{Text: "and?", Reply: &event.Replied{Ref: "7", ByBot: true, At: time.Unix(1, 0).UTC(), Text: strings.Repeat("я", event.MaxRepliedRunes) + "…"}}},
		"reply to a rich message of the bot": {`{` + head + `,"text":"more?","reply_to_message":{"message_id":8,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":7001,"is_bot":true,"first_name":"Bot"},"rich_message":{"blocks":[{"type":"paragraph","text":"report"}]}}}`,
			event.Message{Text: "more?", Reply: &event.Replied{Ref: "8", ByBot: true, At: time.Unix(1, 0).UTC(), Text: "report"}}},
		"reply to a rich message of another bot": {`{` + head + `,"text":"more?","reply_to_message":{"message_id":9,"date":1,"chat":{"id":42,"type":"private"},"from":{"id":8002,"is_bot":true,"first_name":"Other"},"rich_message":{"blocks":[{"type":"paragraph","text":"report"}]}}}`,
			event.Message{Text: "more?", Reply: &event.Replied{Ref: "9", At: time.Unix(1, 0).UTC(), Text: "report", Forwarded: true}}},
		"null forward origin": {`{` + head + `,"text":"hi","forward_origin":null}`,
			event.Message{Text: "hi"}},
	} {
		var u update
		if err := json.Unmarshal([]byte(`{"update_id":9,"message":`+tc.raw+`}`), &u); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		e, ok := tr.toEvent(7001, u)
		if !ok {
			t.Fatalf("%s: no event", name)
		}
		var got event.Message
		if err := json.Unmarshal(e.Payload, &got); err != nil {
			t.Fatal(err)
		}
		tc.want.From, tc.want.Name, tc.want.Ref = "telegram:user:42", "Igor", "5"
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %+v, want %+v", name, got, tc.want)
		}
	}
}
