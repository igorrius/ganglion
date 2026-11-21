package backup

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
)

// Runner makes one backup; *Backup is one.
type Runner interface {
	Run(ctx context.Context) (Result, error)
	// RunRequest is Run for a backup a command asked for; the id is
	// recorded with the result.
	RunRequest(ctx context.Context, request string) (Result, error)
}

// AfterFunc waits for d: its channel fires after d (time.After).
type AfterFunc func(d time.Duration) <-chan time.Time

// EnqueueFunc stores messages in the outbox (queue.Enqueue).
type EnqueueFunc func(ctx context.Context, msgs ...event.Outgoing) error

// LangFunc is a person's language (language.Store.Resolve).
type LangFunc func(ctx context.Context, principal string) (texts.Lang, error)

// ParseAt reads settings' HH:MM into the time since midnight.
func ParseAt(s string) (time.Duration, error) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, errors.New("backup: at must be HH:MM")
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// Daily is the node component that makes a backup each day at a wall time
// of the node's zone (R-BAK-1), one at a time and none at start. A failure
// tells the admins once, however many days it lasts, and so does the
// recovery (R-BAK-5).
type Daily struct {
	run     Runner
	dir     string
	at      time.Duration
	admins  alert.Admins
	enqueue EnqueueFunc
	lang    LangFunc
	texts   *texts.Catalog
	after   AfterFunc
	poll    AfterFunc
	now     func() time.Time
	loc     *time.Location
	log     *slog.Logger
	tel     telemetry
	// failing is whether the admins were told of a failure that has not
	// recovered. Only the Run goroutine touches it.
	failing bool
}

// DailyOption configures a Daily.
type DailyOption func(*Daily)

// WithDailyAfter sets how the wait for the next run is made (tests).
func WithDailyAfter(f AfterFunc) DailyOption { return func(d *Daily) { d.after = f } }

// WithDailyPoll sets how the wait between two looks for a request file is
// made (tests).
func WithDailyPoll(f AfterFunc) DailyOption { return func(d *Daily) { d.poll = f } }

// WithDailyClock sets the clock the next run is computed from (tests).
func WithDailyClock(f func() time.Time) DailyOption { return func(d *Daily) { d.now = f } }

// WithDailyLocation sets the time zone the run's time of day is in; UTC by
// default.
func WithDailyLocation(l *time.Location) DailyOption { return func(d *Daily) { d.loc = l } }

// NewDaily makes the daily component: run is called at the time since
// midnight at in the node's zone, and the result is recorded in the data
// directory dir.
func NewDaily(run Runner, dir string, at time.Duration, admins alert.Admins, enqueue EnqueueFunc, lang LangFunc,
	cat *texts.Catalog, log *slog.Logger, opts ...DailyOption) *Daily {
	d := &Daily{run: run, dir: dir, at: at, admins: admins, enqueue: enqueue, lang: lang, texts: cat,
		after: time.After, poll: time.After, now: time.Now, loc: time.UTC, log: log, tel: newTelemetry()}
	for _, o := range opts {
		o(d)
	}
	return d
}

// Name implements node.Component.
func (d *Daily) Name() string { return "backup" }

// Run waits for each day's time and makes the backup until ctx ends. It
// does not run at start: a restart loop must not upload a copy each time.
// A failed backup is told and logged and waits for the next day; it never
// stops the node. Meanwhile it looks every RequestPoll for the request file
// of `ganglion backup now` and makes that backup as it would at its time.
func (d *Daily) Run(ctx context.Context) error {
	last, err := Last(d.dir)
	switch {
	case err == nil:
		d.failing = !last.OK
		d.tel.last(ctx, last)
	case !errors.Is(err, ErrNoResult):
		d.log.Warn("backup: the last result is not readable", "err", err)
	}
	for {
		if !d.waitForDay(ctx, d.after(d.untilNext())) {
			return nil
		}
		d.once(ctx, "")
	}
}

// waitForDay waits for the day's time to come, serving a backup request
// that arrives meanwhile. It reports false when ctx ended first.
func (d *Daily) waitForDay(ctx context.Context, day <-chan time.Time) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-day:
			return true
		case <-d.poll(RequestPoll):
			d.serve(ctx)
		}
	}
}

// serve makes the backup a request file asks for, if there is one, and
// removes the file once the result with its id is recorded. A request the
// last result already answers (the node stopped before it removed the file)
// is only removed; one the run did not finish (shutdown, another backup
// running) stays for the next look.
func (d *Daily) serve(ctx context.Context) {
	id, ok := readRequest(d.dir)
	if !ok {
		return
	}
	if last, err := Last(d.dir); err == nil && last.Request == id {
		dropRequest(d.dir, id)
		return
	}
	if d.once(ctx, id) {
		dropRequest(d.dir, id)
	}
}

// untilNext is the time from now to the next run.
func (d *Daily) untilNext() time.Duration {
	now := d.now()
	return d.nextAfter(now).Sub(now)
}

// nextAfter is the first wall time at after t in the node's zone. The date
// is built each day from the clock reading, so a day of 23 or 25 hours
// does not move the run, and a time that does not exist on a day the clocks
// jump is the zone's own reading of it.
func (d *Daily) nextAfter(t time.Time) time.Time {
	y, m, day := t.In(d.loc).Date()
	h, mi := int(d.at/time.Hour), int(d.at%time.Hour/time.Minute)
	next := time.Date(y, m, day, h, mi, 0, 0, d.loc)
	if !next.After(t) {
		next = time.Date(y, m, day+1, h, mi, 0, 0, d.loc)
	}
	return next
}

// once makes one backup, for the request with the id request or for the
// day when it is "", and tells the admins when a failure begins or ends. It
// reports whether the backup ran to an end, which a shutdown or another
// backup in progress prevents.
func (d *Daily) once(ctx context.Context, request string) (ran bool) {
	res, err := d.run.RunRequest(ctx, request)
	if ctx.Err() != nil {
		return false // stopped by shutdown: neither a failure nor a success
	}
	if errors.Is(err, ErrBusy) {
		d.log.Info("backup: skipped, another backup of this node is running")
		return false
	}
	d.tel.ran(ctx, res)
	if err != nil && res.OK {
		d.log.Warn("backup: the result was not recorded", "err", err)
		return false
	}
	if err != nil {
		d.log.Warn("backup: failed", "kind", res.Kind, "err", err)
	} else {
		d.log.Info("backup: done", "key", res.Key, "bytes", res.Size)
	}
	switch {
	case !res.OK && !d.failing:
		d.failing = d.tell(ctx, d.failedText(res), "backup:failed:"+stamp(res.LastOK))
	case res.OK && d.failing:
		d.failing = !d.tell(ctx, d.recoveredText(res), "backup:recovered:"+stamp(res.Time))
	}
	return true
}

// stamp is a time in an idempotency key: stable for the same instant.
func stamp(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	return strconv.FormatInt(t.Unix(), 10)
}

// failedText is the alert's text for each admin: the reason in fixed words
// and the last good copy, never an error's text (R-BAK-3).
func (d *Daily) failedText(res Result) alert.TextFunc {
	return func(ctx context.Context, principal string) string {
		l := d.langOf(ctx, principal)
		last := d.texts.Text(l, texts.BackupLastNone, nil)
		if !res.LastOK.IsZero() {
			last = res.LastOK.In(d.loc).Format("2006-01-02 15:04")
		}
		return d.texts.Text(l, texts.BackupAlertFailed, texts.Vars{"reason": d.texts.Text(l, ReasonKey(res.Kind),
			texts.Vars{"detail": texts.Escape(res.Detail)}), "last": last})
	}
}

// recoveredText is the alert's text for the recovery: the copy's size and time.
func (d *Daily) recoveredText(res Result) alert.TextFunc {
	return func(ctx context.Context, principal string) string {
		return d.texts.Text(d.langOf(ctx, principal), texts.BackupAlertRecovered,
			texts.Vars{"size": status.Bytes(res.Size), "time": res.Time.In(d.loc).Format("2006-01-02 15:04")})
	}
}

// tell stores text for every admin and reports whether it was stored. The
// key is the same for the same streak, so a repeat is sent once.
func (d *Daily) tell(ctx context.Context, text alert.TextFunc, key string) bool {
	msgs, err := d.admins.Each(ctx, key, text)
	if err == nil && len(msgs) > 0 {
		err = d.enqueue(ctx, msgs...)
	}
	if err != nil {
		if ctx.Err() == nil {
			d.log.Error("backup: the admins were not told", "key", key, "err", err)
		}
		return false
	}
	return true
}

func (d *Daily) langOf(ctx context.Context, principal string) texts.Lang {
	l, err := d.lang(ctx, principal)
	if err != nil {
		return texts.English
	}
	return l
}

// ReasonKey is the text that says why a backup failed.
func ReasonKey(f Failure) texts.Key {
	switch f {
	case FailureRefused:
		return texts.BackupReasonRefused
	case FailureNetwork:
		return texts.BackupReasonNetwork
	case FailureChanged:
		return texts.BackupReasonChanged
	case FailureIntegrity:
		return texts.BackupReasonIntegrity
	case FailureNone, FailureOther:
	}
	return texts.BackupReasonOther
}
