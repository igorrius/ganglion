package telegram

import (
	"context"
	"encoding/json"
)

// TopicFunc is told the title of a forum topic when the transport first
// sees it or sees it change. group is the forum's key (GroupKey) and
// thread the topic's id. renamed is true for a title that came from a
// rename: it is newer than the one the topic's root still carries, which
// is the title the topic was created with. The title is outside text.
type TopicFunc func(ctx context.Context, group string, thread int64, title string, renamed bool)

// WithTopics sets who is told the titles of forum topics; the default is
// nobody.
func WithTopics(f TopicFunc) Option { return func(c *config) { c.topics = f } }

func noTopics(context.Context, string, int64, string, bool) {}

// topicKey names a forum topic.
type topicKey struct{ chat, thread int64 }

// topicTitle is the title last told of a topic.
type topicTitle struct {
	title   string
	renamed bool
}

// forumTopic is what a Bot API ForumTopicCreated or ForumTopicEdited says
// of the topic's name; an edit that changed only the icon has none.
type forumTopic struct {
	Name string `json:"name"`
}

// topicName is the name in a forum_topic_created or forum_topic_edited
// object; "" when the object is missing or holds none.
func topicName(raw json.RawMessage) string {
	if !present(raw) {
		return ""
	}
	var ft forumTopic
	if json.Unmarshal(raw, &ft) != nil {
		return ""
	}
	return ft.Name
}

// titleOf is the title a message tells of its topic and whether it is a
// rename: the service message that renamed the topic, the one that created
// it, or the topic's root, which Telegram names as the replied message of
// every message in the topic that is no explicit reply.
func titleOf(m *message) (title string, renamed bool) {
	if name := topicName(m.ForumTopicEdited); name != "" {
		return name, true
	}
	if name := topicName(m.ForumTopicCreated); name != "" {
		return name, false
	}
	if r := m.ReplyToMessage; r != nil {
		return topicName(r.ForumTopicCreated), false
	}
	return "", false
}

// noteTopic tells t.topics the title of the forum topic a message is in,
// once per title: also of a message that becomes no event, such as one
// that does not address the bot. A title from the root never replaces one
// from a rename, which is newer.
func (t *Transport) noteTopic(ctx context.Context, u update) {
	m := u.Message
	if m == nil || !isGroup(m.Chat) || m.MessageThreadID == 0 {
		return
	}
	title, renamed := titleOf(m)
	if title == "" {
		return
	}
	key := topicKey{chat: m.Chat.ID, thread: m.MessageThreadID}
	t.mu.Lock()
	old := t.titles[key]
	fresh := old.title != title && (renamed || !old.renamed)
	if fresh {
		t.titles[key] = topicTitle{title: title, renamed: renamed}
	}
	t.mu.Unlock()
	if fresh {
		t.topics(ctx, GroupKey(m.Chat.ID), m.MessageThreadID, title, renamed)
	}
}
