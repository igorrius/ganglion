package telegram

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

// topicLog records what the transport told of topics.
type topicLog struct {
	mu  sync.Mutex
	got []string
}

func (l *topicLog) seen(_ context.Context, group string, thread int64, title string, renamed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.got = append(l.got, fmt.Sprintf("%s/%d %q renamed=%v", group, thread, title, renamed))
}

func (l *topicLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.got)
}

// The transport tells a topic's title once, from the topic's root, also
// for a message that does not address the bot; a rename replaces it and
// the root's older title does not come back (R-CFG-12).
func TestTopicTitlesAreTold(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	topics := &topicLog{}
	tr, err := New(testToken, WithBaseURL(fake.URL()), WithPollTimeout(time.Second), WithTopics(topics.seen))
	if err != nil {
		t.Fatal(err)
	}
	c := &collector{}
	run(t, tr, c.sink)
	in := telegramtest.Incoming{ChatID: -1001, ThreadID: 45, UserID: 9, Text: "nice weather", TopicTitle: "K7"}
	fake.AddMessage(in)
	fake.AddMessage(in)
	fake.AddTopicEdited(-1001, 45, "K7 reports")
	fake.AddMessage(in) // its root still says K7
	// No topic, no forum, a private chat: nothing to tell.
	fake.AddMessage(telegramtest.Incoming{ChatID: -1001, UserID: 9, Text: "general"})
	fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "private", TopicTitle: "x"})
	last := fake.AddMessage(telegramtest.Incoming{ChatID: 42, UserID: 42, Text: "done"})
	eventually(t, "the updates to be handled", func() bool { return len(c.got()) == 2 && fake.Confirmed() >= last-1 })
	want := []string{`telegram:-1001/45 "K7" renamed=false`, `telegram:-1001/45 "K7 reports" renamed=true`}
	if got := topics.all(); !slices.Equal(got, want) {
		t.Fatalf("told %v, want %v", got, want)
	}
}

func TestTitleOf(t *testing.T) {
	created := []byte(`{"name":"K7","icon_color":1}`)
	for name, tc := range map[string]struct {
		m       *message
		title   string
		renamed bool
	}{
		"the message that created the topic": {&message{ForumTopicCreated: created}, "K7", false},
		"a rename":                           {&message{ForumTopicEdited: []byte(`{"name":"New"}`)}, "New", true},
		"an icon change":                     {&message{ForumTopicEdited: []byte(`{"icon_custom_emoji_id":"1"}`)}, "", false},
		"a message under the root":           {&message{ReplyToMessage: &message{ForumTopicCreated: created}}, "K7", false},
		"a reply to a plain message":         {&message{ReplyToMessage: &message{Text: "hi"}}, "", false},
		"a plain message":                    {&message{Text: "hi"}, "", false},
		"a name that is not a string":        {&message{ForumTopicCreated: []byte(`{"name":7}`)}, "", false},
	} {
		if title, renamed := titleOf(tc.m); title != tc.title || renamed != tc.renamed {
			t.Errorf("%s: %q %v, want %q %v", name, title, renamed, tc.title, tc.renamed)
		}
	}
}
