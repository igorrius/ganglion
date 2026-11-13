package telegram

import (
	"encoding/json"
	"testing"
)

func TestAddressed(t *testing.T) {
	const bot, name = int64(7), "family_bot"
	mention := func(text string, off, n int) *message {
		return &message{Text: text, Entities: []inEntity{{Type: "mention", Offset: off, Length: n}}}
	}
	tests := []struct {
		name string
		m    *message
		want bool
	}{
		{"plain text", &message{Text: "what a day"}, false},
		{"a mention of the bot", mention("@family_bot hi", 0, 11), true},
		{"the mention in another case", mention("@Family_Bot hi", 0, 11), true},
		{"a mention after an emoji counts UTF-16 units", mention("😀 @family_bot", 3, 11), true},
		{"a mention of someone else", mention("@family_bot2 hi", 0, 12), false},
		{"the bot's name typed with no entity", &message{Text: "@family_bot hi"}, false},
		{"a text mention of the bot", &message{Text: "Bot hi", Entities: []inEntity{{Type: "text_mention", Offset: 0, Length: 3, User: &user{ID: bot}}}}, true},
		{"a text mention of a person", &message{Text: "Ann hi", Entities: []inEntity{{Type: "text_mention", Offset: 0, Length: 3, User: &user{ID: 9}}}}, false},
		{"a command with the bot's name", &message{Text: "/new@family_bot", Entities: []inEntity{{Type: "bot_command", Offset: 0, Length: 15}}}, true},
		{"a bare command", &message{Text: "/new", Entities: []inEntity{{Type: "bot_command", Offset: 0, Length: 4}}}, false},
		{"a command for another bot", &message{Text: "/new@other_bot", Entities: []inEntity{{Type: "bot_command", Offset: 0, Length: 14}}}, false},
		{"a mention in a caption", &message{Caption: "@family_bot look", CaptionEntities: []inEntity{{Type: "mention", Offset: 0, Length: 11}}}, true},
		{"a reply to the bot", &message{Text: "and tomorrow?", ReplyToMessage: &message{MessageID: 5, From: &user{ID: bot, IsBot: true}}}, true},
		{"a reply to a person", &message{Text: "yes", ReplyToMessage: &message{MessageID: 5, From: &user{ID: 9}}}, false},
		{"an entity past the end of the text", mention("hi", 5, 11), false},
		{"an entity with a negative offset", mention("@family_bot", -1, 11), false},
		{"an entity with a length that overflows", mention("@family_bot", 1, int(^uint(0)>>1)), false},
		{"a link whose text is the bot's name", &message{Text: "@family_bot", Entities: []inEntity{{Type: "text_link", Offset: 0, Length: 11}}}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := addressed(tt.m, bot, name); got != tt.want {
				t.Fatalf("addressed = %v, want %v", got, tt.want)
			}
		})
	}
}

// With no username known, nothing typed can address the bot.
func TestAddressedWithoutAUsername(t *testing.T) {
	m := &message{Text: "@ hi /x@", Entities: []inEntity{{Type: "mention", Offset: 0, Length: 1}, {Type: "bot_command", Offset: 5, Length: 3}}}
	if addressed(m, 7, "") {
		t.Fatal("an empty username was addressed")
	}
}

// A topic the bot itself created: every message in it names the bot's
// service message as the replied one. That is no reply to the bot.
func TestTopicRootByTheBotIsNotAddressing(t *testing.T) {
	m := &message{Text: "hello all", IsTopicMessage: true, MessageThreadID: 45,
		ReplyToMessage: &message{MessageID: 45, From: &user{ID: 7, IsBot: true}, ForumTopicCreated: json.RawMessage(`{"name":"K7"}`)}}
	if addressed(m, 7, "family_bot") {
		t.Fatal("the topic's root counted as a reply to the bot")
	}
}

func TestIsGroup(t *testing.T) {
	for typ, want := range map[string]bool{"private": false, "group": true, "supergroup": true, "channel": false, "": false} {
		if got := isGroup(chat{Type: typ}); got != want {
			t.Errorf("isGroup(%q) = %v, want %v", typ, got, want)
		}
	}
}
