package groups_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/igorrius/ganglion/groups"
)

func newTopics(t *testing.T, s *groups.Store, connected map[string]bool, fail error) groups.Topics {
	t.Helper()
	return groups.NewTopics(s, func(_ context.Context, g string) (bool, error) { return connected[g], fail },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func titles(t *testing.T, s *groups.Store, group string) []string {
	t.Helper()
	ts, err := s.Topics(context.Background(), group)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, len(ts))
	for i, tp := range ts {
		out[i] = tp.Title
	}
	return out
}

func TestTopicsOfAConnectedGroupAreRemembered(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, &clock{at: t0})
	tp := newTopics(t, s, map[string]bool{family: true}, nil)
	tp.Seen(ctx, family, 45, "K7", false)
	tp.Seen(ctx, family, 8, "Home", false)
	tp.Seen(ctx, "telegram:-1002", 3, "Strangers", false) // not connected: not remembered
	if got := titles(t, s, family); strings.Join(got, ",") != "Home,K7" {
		t.Fatalf("topics %v", got)
	}
	if got := titles(t, s, "telegram:-1002"); len(got) != 0 {
		t.Fatalf("a group that is not connected has topics %v", got)
	}
	one, ok, err := s.Topic(ctx, family, 45)
	if err != nil || !ok || one != (groups.Topic{Group: family, Thread: 45, Title: "K7"}) {
		t.Fatalf("topic %+v %v %v", one, ok, err)
	}
	if _, ok, _ := s.Topic(ctx, family, 99); ok {
		t.Fatal("a topic nobody saw is known")
	}
	if err := s.ForgetTopics(ctx, family); err != nil || len(titles(t, s, family)) != 0 {
		t.Fatalf("after ForgetTopics: %v, %v", err, titles(t, s, family))
	}
}

// A rename is newer than the title the topic's root carries: the root's
// title does not come back after it, a later rename does replace it.
func TestARenameWinsOverTheRootsTitle(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, &clock{at: t0})
	tp := newTopics(t, s, map[string]bool{family: true}, nil)
	tp.Seen(ctx, family, 45, "Topic", false)
	tp.Seen(ctx, family, 45, "K7", true)
	tp.Seen(ctx, family, 45, "Topic", false)
	if got := titles(t, s, family); len(got) != 1 || got[0] != "K7" {
		t.Fatalf("topics %v, want the renamed title", got)
	}
	tp.Seen(ctx, family, 45, "K7 reports", true)
	if got := titles(t, s, family); got[0] != "K7 reports" {
		t.Fatalf("topics %v", got)
	}
}

// Abuse: a title is the group's own text and a member can make topics
// without end: a title is cut and a group holds a bounded number.
func TestTopicsAreBounded(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, &clock{at: t0})
	tp := newTopics(t, s, map[string]bool{family: true}, nil)
	tp.Seen(ctx, family, 1, strings.Repeat("я", 500), false)
	if n := len([]rune(titles(t, s, family)[0])); n > 129 {
		t.Fatalf("a stored title has %d runes", n)
	}
	for i := range groups.MaxTopics + 20 {
		tp.Seen(ctx, family, int64(100+i), "t", false)
	}
	if n := len(titles(t, s, family)); n != groups.MaxTopics {
		t.Fatalf("%d topics, want %d", n, groups.MaxTopics)
	}
	// A topic already known is still updated at the cap.
	tp.Seen(ctx, family, 1, "renamed", true)
	if got, _, _ := s.Topic(ctx, family, 1); got.Title != "renamed" {
		t.Fatalf("a known topic was not updated at the cap: %+v", got)
	}
}

// The hook never fails a message: an error reading the groups stores nothing.
func TestTopicsSurviveAnError(t *testing.T) {
	s := newStore(t, &clock{at: t0})
	newTopics(t, s, map[string]bool{family: true}, errors.New("the file is broken")).Seen(context.Background(), family, 45, "K7", false)
	if got := titles(t, s, family); len(got) != 0 {
		t.Fatalf("topics %v stored while the groups could not be read", got)
	}
}

// A title the admin gave stands against the root's older one and gives
// way to a real rename.
func TestNameTopicIsARename(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, &clock{at: t0})
	tp := newTopics(t, s, map[string]bool{family: true}, nil)
	if err := s.NameTopic(ctx, family, 8, "K7"); err != nil {
		t.Fatal(err)
	}
	tp.Seen(ctx, family, 8, "Topic", false) // the root's first title
	if got, _, _ := s.Topic(ctx, family, 8); got.Title != "K7" {
		t.Fatalf("the admin's title was replaced by the root's: %+v", got)
	}
	tp.Seen(ctx, family, 8, "K7 reports", true)
	if got, _, _ := s.Topic(ctx, family, 8); got.Title != "K7 reports" {
		t.Fatalf("a rename did not replace the admin's title: %+v", got)
	}
}
