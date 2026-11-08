package sqlqueue_test

import (
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// queueLine is the queues' one line at f's clock.
func queueLine(t *testing.T, f fixture) status.Check {
	t.Helper()
	cs := f.q.Status(t.Context(), status.Scope{Now: f.clock.Now(), Location: time.UTC})
	if len(cs) != 1 || cs[0].Section != status.SectionDelivery || cs[0].Line != texts.StatusQueues {
		t.Fatalf("lines %+v", cs)
	}
	return cs[0]
}

func inboxRow(t *testing.T, f fixture, id string, at time.Time, state string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO inbox (id, idem_key, source, conv, kind, at_ms, status) VALUES (?, ?, 'telegram', 'c', 'message', ?, ?)`,
		id, id, at.UnixMilli(), state); err != nil {
		t.Fatal(err)
	}
}

func outboxRow(t *testing.T, f fixture, id string, created, done time.Time, state string) {
	t.Helper()
	if _, err := f.db.Exec(`INSERT INTO outbox (id, idem_key, transport, conv, body, status, created_ms, done_ms) VALUES (?, ?, 'telegram', 'c', '{}', ?, ?, ?)`,
		id, id, state, created.UnixMilli(), done.UnixMilli()); err != nil {
		t.Fatal(err)
	}
}

func TestStatusEmptyQueues(t *testing.T) {
	f := setup(t)
	c := queueLine(t, f)
	if c.State != status.OK || c.Vars["inbox"] != "0" || c.Vars["outbox"] != "0" {
		t.Errorf("%+v", c)
	}
}

func TestStatusOldWaiting(t *testing.T) {
	f := setup(t)
	inboxRow(t, f, "a", t0.Add(-2*time.Minute), "pending")
	inboxRow(t, f, "later", t0.Add(time.Hour), "pending") // a scheduled event not yet due
	inboxRow(t, f, "done", t0.Add(-time.Hour), "done")
	outboxRow(t, f, "o", t0.Add(-10*time.Second), time.Time{}, "pending")
	c := queueLine(t, f)
	if c.State != status.Warn || c.Reason != texts.StatusReasonQueueOld || c.Vars["for"] != "2 min" ||
		c.Vars["inbox"] != "1" || c.Vars["outbox"] != "1" || !c.Since.Equal(t0.Add(-2*time.Minute)) {
		t.Errorf("%+v", c)
	}
}

func TestStatusUndelivered(t *testing.T) {
	f := setup(t)
	outboxRow(t, f, "failed", t0.Add(-2*time.Hour), t0.Add(-time.Hour), "failed")
	outboxRow(t, f, "old", t0.Add(-49*time.Hour), t0.Add(-48*time.Hour), "failed")
	c := queueLine(t, f)
	if c.State != status.Warn || c.Reason != texts.StatusReasonUndelivered || c.Vars["n"] != "1" {
		t.Errorf("%+v", c)
	}
}

// The message being handled, /status itself among them, does not wait.
func TestStatusLeasedDoesNotWait(t *testing.T) {
	f := setup(t)
	inboxRow(t, f, "a", t0.Add(-2*time.Minute), "pending")
	if _, err := f.db.Exec(`UPDATE inbox SET lease_token = 'x', lease_until_ms = ? WHERE id = 'a'`, t0.Add(time.Minute).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if c := queueLine(t, f); c.State != status.OK || c.Vars["inbox"] != "0" {
		t.Errorf("%+v", c)
	}
}
