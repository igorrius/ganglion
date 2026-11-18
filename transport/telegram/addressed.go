package telegram

import (
	"strings"
	"unicode/utf16"
)

// Bot API values read by the addressing rule.
const (
	chatPrivate    = "private"
	chatGroup      = "group"
	chatSupergroup = "supergroup"

	entityMention     = "mention"      // @username
	entityTextMention = "text_mention" // a name that links to a user with no username
	entityBotCommand  = "bot_command"  // /cmd or /cmd@username
)

// isGroup reports whether c is a group or a supergroup, a forum included.
func isGroup(c chat) bool { return c.Type == chatGroup || c.Type == chatSupergroup }

// addressed reports whether m, a message in a group, addresses the bot
// (R-DLV-16): it mentions the bot, replies to a message the bot sent, or
// is a command that carries the bot's name. A bare command does not count:
// in a group with several bots it goes to all of them. The bot's name
// typed with no entity does not count either: Telegram marks a real
// mention, and a rule that read the text could be fooled by a quote.
func addressed(m *message, botID int64, username string) bool {
	if r := m.ReplyToMessage; r != nil && r.From != nil && r.From.ID == botID && !topicRoot(m, r) {
		return true
	}
	text, entities := m.Text, m.Entities
	if text == "" {
		text, entities = m.Caption, m.CaptionEntities
	}
	units := utf16.Encode([]rune(text))
	for _, e := range entities {
		if names(e, units, botID, username) {
			return true
		}
	}
	return false
}

// names reports whether the entity e of the text names the bot.
func names(e inEntity, units []uint16, botID int64, username string) bool {
	switch e.Type {
	case entityTextMention:
		return e.User != nil && e.User.ID == botID
	case entityMention:
		return username != "" && strings.EqualFold(part(units, e), "@"+username)
	case entityBotCommand:
		_, to, ok := strings.Cut(part(units, e), "@")
		return ok && username != "" && strings.EqualFold(to, username)
	}
	return false
}

// part is the text an entity marks; "" for an entity outside the text.
func part(units []uint16, e inEntity) string {
	if e.Offset < 0 || e.Length <= 0 || e.Offset > len(units)-e.Length {
		return ""
	}
	return string(utf16.Decode(units[e.Offset : e.Offset+e.Length]))
}
