package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/alert"
	"github.com/igorrius/ganglion/internal/ghfetch"
	"github.com/igorrius/ganglion/texts"
)

// The daily check's timing (R-UPD-6). The first check waits so that a
// restart loop never hammers GitHub.
const (
	FirstCheckAfter = 10 * time.Minute
	CheckEvery      = 24 * time.Hour
)

// NotifiedFile is the file in the releases folder that keeps the version
// the admins were last told about. It is not part of state.json: an older
// version refuses a field it does not know there, and a rollback would
// break its launcher.
const NotifiedFile = "notified.json"

// Latester finds the newest stable release (Source).
type Latester interface {
	// Latest is the newest stable core release.
	Latest(ctx context.Context) (Release, error)
}

// notified is the content of NotifiedFile.
type notified struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// Checker is the node component that asks for the newest release once a
// day and tells the admins once per new version. It never installs
// (R-UPD-6). Only the version, the date and a link the node builds reach
// the message (R-UPD-7).
type Checker struct {
	src     Latester
	dir     string
	running string
	admins  alert.Admins
	enqueue EnqueueFunc
	lang    LangFunc
	texts   *texts.Catalog
	after   AfterFunc
	now     func() time.Time
	loc     *time.Location
	log     *slog.Logger
	tel     telemetry
}

// CheckerOption configures a Checker.
type CheckerOption func(*Checker)

// WithCheckAfter sets how the checks are waited for (tests).
func WithCheckAfter(f AfterFunc) CheckerOption { return func(c *Checker) { c.after = f } }

// WithCheckClock sets the clock that stamps the record (tests).
func WithCheckClock(f func() time.Time) CheckerOption { return func(c *Checker) { c.now = f } }

// WithCheckLocation sets the time zone the release's date is shown in;
// UTC by default.
func WithCheckLocation(l *time.Location) CheckerOption { return func(c *Checker) { c.loc = l } }

// NewChecker makes the daily check for the node that runs the core release
// running, whose releases folder is dir.
func NewChecker(src Latester, dir, running string, admins alert.Admins, enqueue EnqueueFunc, lang LangFunc,
	cat *texts.Catalog, log *slog.Logger, opts ...CheckerOption) *Checker {
	c := &Checker{src: src, dir: dir, running: running, admins: admins, enqueue: enqueue, lang: lang, texts: cat,
		after: time.After, now: time.Now, loc: time.UTC, log: log, tel: newTelemetry()}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name implements node.Component.
func (c *Checker) Name() string { return "update-check" }

// Run waits FirstCheckAfter, checks, and checks again every CheckEvery
// until ctx ends. A build without a release version checks nothing. A
// check that fails is logged and waits for the next: it never stops the
// node.
func (c *Checker) Run(ctx context.Context) error {
	running, err := Running(c.running)
	if err != nil {
		c.log.Info("update: the daily check is off for a build without a release version")
		c.tel.check(ctx, checkSkipped)
		return nil
	}
	wait := FirstCheckAfter
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-c.after(wait):
		}
		c.check(ctx, running)
		wait = CheckEvery
	}
}

// check asks for the newest release and tells the admins when it is newer
// than the running one and not the version they were last told about.
func (c *Checker) check(ctx context.Context, running Version) {
	rel, err := c.src.Latest(ctx)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn("update: the daily check failed", "reason", failureReason(err))
			c.tel.check(ctx, checkFailed)
		}
		return
	}
	if Compare(rel.Version, running) <= 0 {
		c.tel.check(ctx, checkCurrent)
		return
	}
	c.tel.check(ctx, checkNewer)
	if c.last().Version == rel.Version.String() {
		return
	}
	if !c.tell(ctx, rel) {
		return
	}
	if err := c.record(rel.Version.String()); err != nil {
		c.log.Warn("update: the version told was not recorded", "release", rel.Tag, "err", err)
		return
	}
	c.log.Info("update: a newer release is out", "release", rel.Tag)
}

// failureReason says why a check failed without any text of the response.
func failureReason(err error) string {
	if se, ok := errors.AsType[*ghfetch.StatusError](err); ok {
		return fmt.Sprintf("GitHub answered %d", se.Code)
	}
	return err.Error()
}

// tell stores the notice for every admin, each in their language, and
// reports whether it was stored.
func (c *Checker) tell(ctx context.Context, rel Release) bool {
	date := rel.Published.In(c.loc).Format("2006-01-02")
	key := "update:available:" + rel.Version.String()
	msgs, err := c.admins.Each(ctx, key, func(ctx context.Context, principal string) string {
		return c.texts.Text(c.langOf(ctx, principal), texts.UpdateAvailable, texts.Vars{varVersion: texts.Escape(rel.Version.String()),
			"date": date, "page": rel.Page})
	})
	if err == nil && len(msgs) > 0 {
		err = c.enqueue(ctx, msgs...)
	}
	if err != nil {
		if ctx.Err() == nil {
			c.log.Error("update: the admins were not told", "key", key, "err", err)
		}
		return false
	}
	return true
}

func (c *Checker) langOf(ctx context.Context, principal string) texts.Lang {
	l, err := c.lang(ctx, principal)
	if err != nil {
		return texts.English
	}
	return l
}

// last is the version the admins were last told about; a missing or an
// unreadable file means none (an unreadable one is logged).
func (c *Checker) last() notified {
	b, err := os.ReadFile(filepath.Join(c.dir, NotifiedFile))
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			c.log.Warn("update: the record of the version told is not readable", "err", err)
		}
		return notified{}
	}
	var n notified
	if err := json.Unmarshal(b, &n); err != nil {
		c.log.Warn("update: the record of the version told is not readable", "err", err)
		return notified{}
	}
	return n
}

// record writes NotifiedFile atomically: a temporary file in the same
// folder, fsync, rename, fsync of the folder. It makes the folder (mode
// 0700) when it is not there.
func (c *Checker) record(version string) (err error) {
	if err := os.MkdirAll(c.dir, 0o700); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	data, err := json.Marshal(notified{Version: version, At: c.now().UTC()})
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	tmp, err := os.CreateTemp(c.dir, ".notified-*")
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(append(data, '\n')); err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if err = os.Rename(tmp.Name(), filepath.Join(c.dir, NotifiedFile)); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	return syncDir(c.dir)
}
