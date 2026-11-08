package telegram

import (
	"testing"
	"time"

	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/transport/telegram/telegramtest"
)

var healthStart = time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

func fixedClock() time.Time { return healthStart }

func line(t *testing.T, h *Health, now time.Time) status.Check {
	t.Helper()
	cs, err := h.Status(t.Context(), status.Scope{Now: now, Location: time.UTC})
	if err != nil || len(cs) != 1 || cs[0].Section != status.SectionDelivery {
		t.Fatalf("lines %+v", cs)
	}
	return cs[0]
}

// healthy is a transport on fake whose health a test reads.
func healthy(t *testing.T, fake *telegramtest.Server, token string) (*Transport, *Health) {
	t.Helper()
	h := NewHealth(fixedClock)
	tr, err := New(token, WithBaseURL(fake.URL()), WithPollTimeout(time.Second), WithHealth(h))
	if err != nil {
		t.Fatal(err)
	}
	tr.retry.Min = 10 * time.Millisecond
	return tr, h
}

func TestStatusBeforeAndAfterAPoll(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr, h := healthy(t, fake, testToken)
	if c := line(t, h, healthStart); c.Line != texts.StatusTelegramWaiting || c.State != status.Warn {
		t.Errorf("before: %+v", c)
	}
	run(t, tr, (&collector{}).sink)
	eventually(t, "a quiet poll", func() bool { return fake.Calls("getUpdates") >= 2 })
	c := line(t, h, healthStart)
	if c.Line != texts.StatusTelegram || c.State != status.OK || c.Vars["time"] != "12:00" {
		t.Errorf("after a quiet poll: %+v", c)
	}
}

func TestStatusStalePoll(t *testing.T) {
	h := NewHealth(fixedClock)
	h.polledNow()
	c := line(t, h, healthStart.Add(3*time.Minute))
	if c.State != status.Warn || c.Reason != texts.StatusReasonPollStale || c.Vars["for"] != "3 min" {
		t.Errorf("stale: %+v", c)
	}
}

func TestStatusAfter409(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	fake.SetWebhook(true)
	tr, h := healthy(t, fake, testToken)
	_, result := run(t, tr, (&collector{}).sink)
	_ = result()
	c := line(t, h, healthStart.Add(time.Minute))
	if c.State != status.Fail || c.Reason != texts.StatusReasonConflict || !c.Since.Equal(healthStart) {
		t.Errorf("409: %+v", c)
	}
}

func TestStatusAfter401(t *testing.T) {
	fake := telegramtest.NewServer(t, testToken)
	tr, h := healthy(t, fake, "7001:wrong")
	_, result := run(t, tr, (&collector{}).sink)
	_ = result()
	if c := line(t, h, healthStart); c.State != status.Fail || c.Reason != texts.StatusReasonUnauthorized {
		t.Errorf("401: %+v", c)
	}
}
