package access_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/igorrius/ganglion/access"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/sqlitedb"
	"github.com/igorrius/ganglion/telemetry/telemetrytest"
	"github.com/igorrius/ganglion/transport"
)

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func newList(t *testing.T) *access.List {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l, err := access.New(ctx, db, access.WithClock(func() time.Time { return t0 }))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func message(from, name, text string) event.Event {
	p, _ := json.Marshal(event.Message{Text: text, From: from, Name: name})
	return event.Event{ID: "01X", IdemKey: "telegram:7001:1", Source: "telegram", Conv: "telegram:42", Kind: "message", Payload: p, At: t0}
}

// recorder is a sink that records what reaches it.
type recorder struct{ got []event.Event }

func (r *recorder) sink(_ context.Context, e event.Event) error {
	r.got = append(r.got, e)
	return nil
}

func TestAllowAndRole(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if _, ok, err := l.Role(ctx, "telegram:user:1"); err != nil || ok {
		t.Fatalf("unknown principal: ok=%v err=%v", ok, err)
	}
	if err := l.Allow(ctx, "telegram:user:1", access.RoleAdmin, "Igor"); err != nil {
		t.Fatal(err)
	}
	if err := l.Allow(ctx, "telegram:user:2", access.RoleFamily, "Anna"); err != nil {
		t.Fatal(err)
	}
	if r, ok, err := l.Role(ctx, "telegram:user:2"); err != nil || !ok || r != access.RoleFamily {
		t.Fatalf("Role = %q, %v, %v", r, ok, err)
	}
	// Allow is an upsert.
	if err := l.Allow(ctx, "telegram:user:2", access.RoleAdmin, "Anna"); err != nil {
		t.Fatal(err)
	}
	admins, err := l.Admins(ctx)
	if err != nil || !slices.Equal(admins, []string{"telegram:user:1", "telegram:user:2"}) {
		t.Fatalf("Admins = %v, %v", admins, err)
	}
	if err := l.Remove(ctx, "telegram:user:2"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := l.Role(ctx, "telegram:user:2"); ok {
		t.Fatal("removed principal still allowed")
	}
}

func TestUnknownRoleRejected(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if err := l.Allow(ctx, "telegram:user:1", access.Role("owner"), ""); !errors.Is(err, access.ErrUnknownRole) {
		t.Fatalf("Allow with role owner: %v, want ErrUnknownRole", err)
	}
	if err := l.Allow(ctx, "", access.RoleFamily, ""); err == nil {
		t.Fatal("Allow with an empty principal succeeded")
	}
}

func TestFilterPassesKnown(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if err := l.Allow(ctx, "telegram:user:1", access.RoleFamily, "Igor"); err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	sink := l.Filter(r.sink, access.NoGroups(), slog.New(slog.DiscardHandler))
	if err := sink(ctx, message("telegram:user:1", "Igor", "hi")); err != nil {
		t.Fatal(err)
	}
	if len(r.got) != 1 {
		t.Fatalf("next got %d events, want 1", len(r.got))
	}
}

func TestFilterDropsStranger(t *testing.T) {
	l := newList(t)
	var logs bytes.Buffer
	r := &recorder{}
	sink := l.Filter(r.sink, access.NoGroups(), slog.New(slog.NewJSONHandler(&logs, nil)))
	const secret = "my card number is 4111"
	if err := sink(context.Background(), message("telegram:user:666", "Stranger", secret)); !errors.Is(err, transport.ErrDropped) {
		t.Fatalf("Filter returned %v, want ErrDropped so the offset moves on", err)
	}
	if len(r.got) != 0 {
		t.Fatal("a stranger's message reached the inbox")
	}
	out := logs.String()
	if strings.Contains(out, secret) || strings.Contains(out, `"text"`) {
		t.Fatalf("log has the text: %s", out)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rec); err != nil {
		t.Fatalf("one JSON log record expected: %q", out)
	}
	if rec["level"] != "WARN" || rec["principal"] != "telegram:user:666" || rec["name"] != "Stranger" ||
		rec["conv"] != "telegram:42" || rec["text_len"] != float64(len(secret)) {
		t.Fatalf("log record %v", rec)
	}
}

// A press of a button is checked like a message: a stranger who got hold
// of a button id cannot answer a question (design section 5.2).
func TestFilterDropsStrangersPress(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if err := l.Allow(ctx, "telegram:user:1", access.RoleAdmin, "Igor"); err != nil {
		t.Fatal(err)
	}
	r := &recorder{}
	sink := l.Filter(r.sink, access.NoGroups(), slog.New(slog.DiscardHandler))
	press := func(from string) event.Event {
		p, _ := json.Marshal(event.Decision{Button: "01JCQ4ZK7Y3V8M2N6P9R5T1W0X", From: from, Ref: "17"})
		return event.Event{ID: "01Z", IdemKey: "telegram:7001:9", Source: "telegram", Conv: "telegram:42", Kind: event.KindDecision, Payload: p, At: t0}
	}
	// Dropped, so the transport answers the press itself (ruling R40).
	if err := sink(ctx, press("telegram:user:666")); !errors.Is(err, transport.ErrDropped) || len(r.got) != 0 {
		t.Fatalf("stranger's press: err=%v passed=%d", err, len(r.got))
	}
	if err := sink(ctx, press("telegram:user:1")); err != nil || len(r.got) != 1 {
		t.Fatalf("admin's press: err=%v passed=%d", err, len(r.got))
	}
}

func TestFilterDropsMalformedMessage(t *testing.T) {
	l := newList(t)
	r := &recorder{}
	sink := l.Filter(r.sink, access.NoGroups(), slog.New(slog.DiscardHandler))
	e := message("telegram:user:1", "", "")
	e.Payload = json.RawMessage(`not json`)
	if err := sink(context.Background(), e); !errors.Is(err, transport.ErrDropped) || len(r.got) != 0 {
		t.Fatalf("malformed payload: err=%v passed=%d", err, len(r.got))
	}
}

func TestFilterPassesNonMessages(t *testing.T) {
	l := newList(t)
	r := &recorder{}
	sink := l.Filter(r.sink, access.NoGroups(), slog.New(slog.DiscardHandler))
	e := event.Event{ID: "01Y", IdemKey: "cron:x", Source: "cron", Conv: "cron:x", Kind: "cron.tick", At: t0}
	if err := sink(context.Background(), e); err != nil || len(r.got) != 1 {
		t.Fatalf("non-message: err=%v passed=%d", err, len(r.got))
	}
}

func TestFilterPassesNextError(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if err := l.Allow(ctx, "telegram:user:1", access.RoleFamily, ""); err != nil {
		t.Fatal(err)
	}
	boom := errors.New("inbox down")
	sink := l.Filter(func(context.Context, event.Event) error { return boom }, access.NoGroups(), slog.New(slog.DiscardHandler))
	if err := sink(ctx, message("telegram:user:1", "", "hi")); !errors.Is(err, boom) {
		t.Fatalf("Filter = %v, want next's error so the update is redelivered", err)
	}
}

func TestFilterCountsDrops(t *testing.T) {
	rec := telemetrytest.Install(t)
	l := newList(t)
	sink := l.Filter((&recorder{}).sink, access.NoGroups(), slog.New(slog.DiscardHandler))
	_ = sink(context.Background(), message("telegram:user:666", "", "x"))
	bad := message("telegram:user:1", "", "")
	bad.Payload = json.RawMessage(`nope`)
	_ = sink(context.Background(), bad)
	if got := rec.Value(t, "ganglion.access.dropped", map[string]string{"reason": "stranger"}); got != 1 {
		t.Errorf("stranger drops %v", got)
	}
	if got := rec.Value(t, "ganglion.access.dropped", map[string]string{"reason": "malformed"}); got != 1 {
		t.Errorf("malformed drops %v", got)
	}
}

func TestWithAdminSeedsTheAdmin(t *testing.T) {
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	l, err := access.New(ctx, db, access.WithAdmin("telegram:user:42"))
	if err != nil {
		t.Fatal(err)
	}
	if role, ok, err := l.Role(ctx, "telegram:user:42"); err != nil || !ok || role != access.RoleAdmin {
		t.Fatalf("role %q, %v, %v", role, ok, err)
	}
	if _, err := access.New(ctx, db, access.WithAdmin("")); err != nil {
		t.Fatalf("an empty admin is no admin: %v", err)
	}
}

func TestIsAdmin(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	_ = l.Allow(ctx, "telegram:user:1", access.RoleAdmin, "Igor")
	_ = l.Allow(ctx, "telegram:user:2", access.RoleFamily, "Anna")
	for p, want := range map[string]bool{"telegram:user:1": true, "telegram:user:2": false, "telegram:user:3": false} {
		got, err := l.IsAdmin(ctx, p)
		if err != nil || got != want {
			t.Errorf("IsAdmin(%s) = %v, %v; want %v", p, got, err, want)
		}
	}
}

func TestListed(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	_ = l.Allow(ctx, "telegram:user:1", access.RoleAdmin, "Igor")
	_ = l.Allow(ctx, "telegram:user:2", access.RoleFamily, "Anna")
	for p, want := range map[string]bool{"telegram:user:1": true, "telegram:user:2": true, "telegram:user:3": false} {
		got, err := l.Listed(ctx, p)
		if err != nil || got != want {
			t.Errorf("Listed(%s) = %v, %v; want %v", p, got, err, want)
		}
	}
}

// Name is who a listed principal is, for a confirmation question; ""
// for one not listed or listed without a name.
func TestName(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if err := l.Allow(ctx, "telegram:user:2", access.RoleFamily, "Anna"); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]string{"telegram:user:2": "Anna", "telegram:user:9": ""} {
		if got, err := l.Name(ctx, p); err != nil || got != want {
			t.Errorf("Name(%s) = %q, %v; want %q", p, got, err, want)
		}
	}
}

const (
	family = "telegram:-1001"
	topic  = "telegram:-1001/45"
	owner  = "telegram:user:1"
)

// groupMsg is a message in a topic of the group.
func groupMsg(from, group string) event.Event {
	p, _ := json.Marshal(event.Message{Text: "hi", From: from, Name: "Ann", Group: group, GroupTitle: "Family"})
	return event.Event{ID: "01G", IdemKey: "telegram:7001:5", Source: "telegram", Conv: topic, Kind: event.KindMessage, Payload: p, At: t0}
}

// groupsFake is the filter's view of group chats in a test.
type groupsFake struct {
	connected map[string]bool
	hasErr    error
	seenErr   error
	seen      []event.GroupSeen
}

func (g *groupsFake) groups() access.Groups {
	return access.Groups{
		Has: func(_ context.Context, group string) (bool, error) { return g.connected[group], g.hasErr },
		Unknown: func(_ context.Context, s event.GroupSeen) error {
			if g.seenErr != nil {
				return g.seenErr
			}
			g.seen = append(g.seen, s)
			return nil
		},
	}
}

// A stranger in a connected group reaches the sink: the group is the
// access list of its members (R-DLV-17).
func TestFilterPassesAStrangerInAConnectedGroup(t *testing.T) {
	g := &groupsFake{connected: map[string]bool{family: true}}
	r := &recorder{}
	sink := newList(t).Filter(r.sink, g.groups(), slog.New(slog.DiscardHandler))
	if err := sink(context.Background(), groupMsg("telegram:user:9", family)); err != nil || len(r.got) != 1 {
		t.Fatalf("err=%v passed=%d", err, len(r.got))
	}
	if len(g.seen) != 0 {
		t.Fatalf("a connected group was reported: %+v", g.seen)
	}
}

// Abuse: an admin in a group nobody connected is dropped like anyone else,
// and the group is reported with the conversation it happened in.
func TestFilterDropsAndReportsAGroupThatIsNotConnected(t *testing.T) {
	rec := telemetrytest.Install(t)
	l := newList(t)
	ctx := context.Background()
	if err := l.Allow(ctx, owner, access.RoleAdmin, "Igor"); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	g := &groupsFake{}
	r := &recorder{}
	sink := l.Filter(r.sink, g.groups(), slog.New(slog.NewJSONHandler(&logs, nil)))
	if err := sink(ctx, groupMsg(owner, family)); !errors.Is(err, transport.ErrDropped) || len(r.got) != 0 {
		t.Fatalf("err=%v passed=%d", err, len(r.got))
	}
	want := event.GroupSeen{Group: family, Conv: topic, Title: "Family", From: owner, Name: "Ann"}
	if len(g.seen) != 1 || g.seen[0] != want {
		t.Fatalf("reported %+v, want %+v", g.seen, want)
	}
	if got := rec.Value(t, "ganglion.access.dropped", map[string]string{"reason": "group"}); got != 1 {
		t.Errorf("group drops %v", got)
	}
	if strings.Contains(logs.String(), "Family") {
		t.Fatalf("the log has the group's title, which is outside text: %s", logs.String())
	}
}

// A press in a group that is not connected is dropped and reports nothing.
func TestFilterDropsAPressInAnUnknownGroupSilently(t *testing.T) {
	l := newList(t)
	ctx := context.Background()
	if err := l.Allow(ctx, owner, access.RoleAdmin, "Igor"); err != nil {
		t.Fatal(err)
	}
	g := &groupsFake{}
	r := &recorder{}
	sink := l.Filter(r.sink, g.groups(), slog.New(slog.DiscardHandler))
	p, _ := json.Marshal(event.Decision{Button: "01JCQ4ZK7Y3V8M2N6P9R5T1W0X", From: owner, Group: family})
	press := event.Event{ID: "01P", Source: "telegram", Conv: topic, Kind: event.KindDecision, Payload: p, At: t0}
	if err := sink(ctx, press); !errors.Is(err, transport.ErrDropped) || len(r.got) != 0 || len(g.seen) != 0 {
		t.Fatalf("err=%v passed=%d reported=%d", err, len(r.got), len(g.seen))
	}
	g.connected = map[string]bool{family: true}
	if err := sink(ctx, press); err != nil || len(r.got) != 1 {
		t.Fatalf("a press in a connected group: err=%v passed=%d", err, len(r.got))
	}
}

// The bot added to a group never reaches the sink; a group that is not
// connected is reported.
func TestFilterTakesGroupJoined(t *testing.T) {
	ctx := context.Background()
	seen := event.GroupSeen{Group: family, Conv: family, Title: "Family", From: "telegram:user:9", Name: "Ann"}
	p, _ := json.Marshal(seen)
	joined := event.Event{ID: "01J", Source: "telegram", Conv: family, Kind: event.KindGroupJoined, Payload: p, At: t0}
	g := &groupsFake{}
	r := &recorder{}
	sink := newList(t).Filter(r.sink, g.groups(), slog.New(slog.DiscardHandler))
	if err := sink(ctx, joined); !errors.Is(err, transport.ErrDropped) || len(r.got) != 0 {
		t.Fatalf("err=%v passed=%d", err, len(r.got))
	}
	if len(g.seen) != 1 || g.seen[0] != seen {
		t.Fatalf("reported %+v", g.seen)
	}
	g.connected = map[string]bool{family: true}
	if err := sink(ctx, joined); !errors.Is(err, transport.ErrDropped) || len(r.got) != 0 || len(g.seen) != 1 {
		t.Fatalf("a connected group: err=%v passed=%d reported=%d", err, len(r.got), len(g.seen))
	}
	joined.Payload = json.RawMessage(`{"group":""}`)
	if err := sink(ctx, joined); !errors.Is(err, transport.ErrDropped) || len(g.seen) != 1 {
		t.Fatalf("a malformed join: err=%v reported=%d", err, len(g.seen))
	}
}

// Errors are returned, so the transport offers the update again: a failure
// is never read as "connected" or as "dropped".
func TestFilterReturnsGroupErrors(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("the file is broken")
	for name, g := range map[string]*groupsFake{
		"reading the groups": {hasErr: boom},
		"reporting a group":  {seenErr: boom},
	} {
		r := &recorder{}
		sink := newList(t).Filter(r.sink, g.groups(), slog.New(slog.DiscardHandler))
		if err := sink(ctx, groupMsg("telegram:user:9", family)); !errors.Is(err, boom) || errors.Is(err, transport.ErrDropped) || len(r.got) != 0 {
			t.Errorf("%s: err=%v passed=%d", name, err, len(r.got))
		}
	}
}

// A private message is judged by the list alone: connected groups do not
// open the private chats of their members.
func TestFilterPrivateChatUnchanged(t *testing.T) {
	g := &groupsFake{connected: map[string]bool{family: true, "": true}}
	r := &recorder{}
	sink := newList(t).Filter(r.sink, g.groups(), slog.New(slog.DiscardHandler))
	if err := sink(context.Background(), message("telegram:user:9", "Ann", "hi")); !errors.Is(err, transport.ErrDropped) || len(r.got) != 0 {
		t.Fatalf("a stranger's private message: err=%v passed=%d", err, len(r.got))
	}
}
