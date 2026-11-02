package telegram

import (
	"fmt"
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
