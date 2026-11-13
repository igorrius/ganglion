package groups_test

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/igorrius/ganglion/groups"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/sqlitedb"
)

var t0 = time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)

const family = "telegram:-1001"

// clock is a clock a test moves.
type clock struct{ at time.Time }

func (c *clock) now() time.Time { return c.at }

func newStore(t *testing.T, c *clock) *groups.Store {
	t.Helper()
	ctx := context.Background()
	db, err := sqlitedb.Open(ctx, filepath.Join(t.TempDir(), "groups.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	s, err := groups.New(ctx, db, c.now)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func status(t *testing.T, s *groups.Store, group string) (groups.Status, bool) {
	t.Helper()
	st, ok, err := s.Status(context.Background(), group)
	if err != nil {
		t.Fatal(err)
	}
	return st, ok
}

func waiting(t *testing.T, s *groups.Store) int {
	t.Helper()
	n, err := s.Pending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStoreRemembersARequest(t *testing.T) {
	ctx := context.Background()
	s := newStore(t, &clock{at: t0})
	if _, ok := status(t, s, family); ok {
		t.Fatal("a request before any was stored")
	}
	if err := s.Set(ctx, family, "Family", groups.StatusPending); err != nil {
		t.Fatal(err)
	}
	if st, ok := status(t, s, family); !ok || st != groups.StatusPending || waiting(t, s) != 1 {
		t.Fatalf("status %q %v, pending %d", st, ok, waiting(t, s))
	}
	if err := s.Set(ctx, family, "Family", groups.StatusRefused); err != nil {
		t.Fatal(err)
	}
	if st, ok := status(t, s, family); !ok || st != groups.StatusRefused || waiting(t, s) != 0 {
		t.Fatalf("status %q %v, pending %d", st, ok, waiting(t, s))
	}
	if err := s.Forget(ctx, family); err != nil {
		t.Fatal(err)
	}
	if err := s.Forget(ctx, family); err != nil {
		t.Fatalf("forgetting twice: %v", err)
	}
	if _, ok := status(t, s, family); ok {
		t.Fatal("a forgotten request is still known")
	}
}

// A request nobody answers runs out; a refusal never does.
func TestAPendingRequestExpires(t *testing.T) {
	ctx := context.Background()
	c := &clock{at: t0}
	s := newStore(t, c)
	if err := s.Set(ctx, family, "Family", groups.StatusPending); err != nil {
		t.Fatal(err)
	}
	if err := s.Set(ctx, "telegram:-1002", "Other", groups.StatusRefused); err != nil {
		t.Fatal(err)
	}
	c.at = t0.Add(groups.RequestTTL)
	if _, ok := status(t, s, family); !ok {
		t.Fatal("the request ran out before its time")
	}
	c.at = t0.Add(groups.RequestTTL + time.Second)
	if _, ok := status(t, s, family); ok || waiting(t, s) != 0 {
		t.Fatalf("the request did not run out: pending %d", waiting(t, s))
	}
	c.at = t0.Add(10 * groups.RequestTTL)
	if st, ok := status(t, s, "telegram:-1002"); !ok || st != groups.StatusRefused {
		t.Fatalf("a refusal ran out: %q %v", st, ok)
	}
}

func TestConnectedReadsTheListEveryTime(t *testing.T) {
	ctx := context.Background()
	var list []settings.Group
	var fail error
	c := groups.Connected{
		List: func() ([]settings.Group, error) { return list, fail },
		Key:  func(chat int64) string { return "telegram:" + strconv.FormatInt(chat, 10) },
	}
	if ok, err := c.Has(ctx, family); ok || err != nil {
		t.Fatalf("Has = %v, %v before the group is listed", ok, err)
	}
	list = append(list, settings.Group{Chat: -1001, Role: settings.GroupRoleFamily})
	if ok, err := c.Has(ctx, family); !ok || err != nil {
		t.Fatalf("Has = %v, %v after the group is listed", ok, err)
	}
	if ok, _ := c.Has(ctx, "telegram:-1002"); ok {
		t.Fatal("another group is connected")
	}
	if ok, _ := c.Has(ctx, "telegram:-1001/45"); ok {
		t.Fatal("a topic's conversation passed for the group's key")
	}
	fail = errors.New("the file is broken")
	if ok, err := c.Has(ctx, family); ok || err == nil {
		t.Fatalf("Has = %v, %v with a broken file: an error must not read as an answer", ok, err)
	}
}
