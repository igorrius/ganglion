package telegram

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

const convPrefix = "telegram:"

// ConvKey returns the conversation key of a chat, or of a forum topic in
// it when threadID is not 0: "telegram:<chat_id>" or
// "telegram:<chat_id>/<thread_id>".
func ConvKey(chatID, threadID int64) string {
	key := convPrefix + strconv.FormatInt(chatID, 10)
	if threadID != 0 {
		key += "/" + strconv.FormatInt(threadID, 10)
	}
	return key
}

// ParseConv is the inverse of ConvKey.
func ParseConv(conv string) (chatID, threadID int64, err error) {
	rest, ok := strings.CutPrefix(conv, convPrefix)
	if !ok {
		return 0, 0, fmt.Errorf("telegram: conversation %q is not telegram:<chat_id>[/<thread_id>]", conv)
	}
	chatPart, threadPart, hasThread := strings.Cut(rest, "/")
	chatID, err = strconv.ParseInt(chatPart, 10, 64)
	if err != nil || chatID == 0 {
		return 0, 0, fmt.Errorf("telegram: conversation %q: bad chat id", conv)
	}
	if hasThread {
		threadID, err = strconv.ParseInt(threadPart, 10, 64)
		if err != nil || threadID <= 0 {
			return 0, 0, fmt.Errorf("telegram: conversation %q: bad thread id", conv)
		}
	}
	return chatID, threadID, nil
}

// Principal returns the principal of a Telegram user: "telegram:user:<id>".
func Principal(userID int64) string {
	return "telegram:user:" + strconv.FormatInt(userID, 10)
}

// PrivateConv returns the private chat of a Telegram principal (a user's
// private chat has the user's id), with this transport's name. ok is false
// for a principal of another transport or a malformed one.
func PrivateConv(principal string) (transport, conv string, ok bool) {
	idStr, ok := strings.CutPrefix(principal, "telegram:user:")
	if !ok {
		return "", "", false
	}
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return "", "", false
	}
	return Name, ConvKey(id, 0), true
}

// GroupKey is the key of a group chat as a whole: its conversation with
// no topic. Every topic of a forum shares it.
func GroupKey(chatID int64) string { return ConvKey(chatID, 0) }

// GroupChat returns the chat id of a group's key; ok is false for a key
// that is not a group chat as a whole: a private chat, a topic, another
// transport's key.
func GroupChat(group string) (chat int64, ok bool) {
	chat, thread, err := ParseConv(group)
	return chat, err == nil && thread == 0 && chat < 0
}

// topicLink is a link to a topic or to a message of a group that has no
// public name: https://t.me/c/<id>/<topic>[/<message>].
var topicLink = regexp.MustCompile(`^https?://t\.me/c/([0-9]{5,15})/([0-9]{1,10})(?:/[0-9]{1,10})?/?(?:\?[^\s]*)?$`)

// generalTopic is the id Telegram's links give a forum's General topic,
// whose messages carry no topic id.
const generalTopic = 1

// ParseLink returns the conversation a link to a topic, or to a message in
// one, points at: the admin copies the link from Telegram instead of
// knowing ids (R-CFG-12). ok is false for anything else, a link to a group
// with a public name included: its id is not in the link.
func ParseLink(link string) (conv string, ok bool) {
	m := topicLink.FindStringSubmatch(strings.TrimSpace(link))
	if m == nil {
		return "", false
	}
	chat, err := strconv.ParseInt("-100"+m[1], 10, 64)
	if err != nil {
		return "", false
	}
	thread, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return "", false
	}
	if thread == generalTopic {
		thread = 0
	}
	return ConvKey(chat, thread), true
}
