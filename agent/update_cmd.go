package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/update"
)

// Updater is what /update does with the node's core releases. It never
// returns anything a release page says but the version, the date and the
// page the node built from the tag (R-UPD-7).
type Updater interface {
	// Status is the running version and the newest stable release.
	Status(ctx context.Context) (update.Status, error)
	// Install puts a version in place; the node restarts after it.
	Install(ctx context.Context, version string) (string, error)
	// Rollback moves to the previous version, "" for the image's binary.
	Rollback(ctx context.Context) (string, error)
	// RollbackTarget is the version a rollback would go to, "" for the
	// image's binary, and whether there is one; it reads and changes nothing.
	RollbackTarget() (to string, ok bool)
}

// NoUpdater is a node that cannot update itself: every call is refused.
type NoUpdater struct{}

// errNoUpdater is what NoUpdater refuses with.
var errNoUpdater = errors.New("agent: this node cannot update itself")

// Status refuses.
func (NoUpdater) Status(context.Context) (update.Status, error) { return update.Status{}, errNoUpdater }

// Install refuses.
func (NoUpdater) Install(context.Context, string) (string, error) { return "", errNoUpdater }

// Rollback refuses.
func (NoUpdater) Rollback(context.Context) (string, error) { return "", errNoUpdater }

// RollbackTarget has none.
func (NoUpdater) RollbackTarget() (string, bool) { return "", false }

// Words of /update and the names of the values its texts take.
const (
	wordInstall  = "install"
	wordRollback = "rollback"
	varImage     = "image"
	varDate      = "date"

	// updateEarlyKey is the idempotency key suffix of the "installing" reply.
	updateEarlyKey = ":update:early"
	updateNote     = commandPrefix + "the owner ran /update: "
)

// refusalKey is the text that says why err refused an update. Nothing in
// an error's own string is shown.
func refusalKey(err error) texts.Key {
	if errors.Is(err, errNoUpdater) {
		return texts.UpdateUnavailable
	}
	return update.RefusalKey(err)
}

// updateCmd is /update: the running version and the newest release, the
// install of a named version and the rollback to the previous one (R-UPD-2,
// R-UPD-3), for an admin in their private chat, without a model. Naming the
// version is the confirmation.
func (a *Agent) updateCmd(ctx context.Context, t *turn, arg string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	if _, conv, ok := a.d.Private(t.msg.From); !ok || conv != t.event.Conv {
		t.reply(a.say(ctx, t, texts.UpdatePrivateOnly, nil))
		return nil
	}
	words := strings.Fields(arg)
	switch {
	case len(words) == 0:
		a.updateStatus(ctx, t)
	case words[0] == wordInstall && len(words) == 2:
		a.updateInstall(ctx, t, words[1])
	case words[0] == wordRollback && len(words) == 1:
		a.updateRollback(ctx, t)
	default:
		t.reply(a.say(ctx, t, texts.UpdateUsage, nil))
	}
	return nil
}

// updateStatus is the reply of /update on its own, with the buttons of
// what it offers.
func (a *Agent) updateStatus(ctx context.Context, t *turn) {
	st, err := a.d.Updater.Status(ctx)
	if err != nil {
		t.reply(a.refusal(ctx, a.speaker(ctx, t), err))
		return
	}
	if st.Dev {
		t.reply(a.devBuild(a.speaker(ctx, t), st))
		return
	}
	buttons, err := a.updateButtons(ctx, a.speaker(ctx, t), t, st)
	if err != nil {
		a.log.Warn("agent: /update: the buttons were not stored", "err", err)
	}
	t.out = append(t.out, event.Outgoing{IdemKey: t.event.IdemKey + ":reply:0", Transport: t.transport, Conv: t.replyConv,
		Text: a.statusText(ctx, t, st), ReplyTo: t.msg.Ref, Buttons: buttons})
}

// statusText is the text of the status of a release build.
func (a *Agent) statusText(ctx context.Context, t *turn, st update.Status) string {
	var b strings.Builder
	b.WriteString(a.say(ctx, t, texts.UpdateStatus, texts.Vars{"running": texts.Escape(st.Running.String()),
		"newest": texts.Escape(st.Newest.Version.String()), varDate: st.Newest.Published.In(a.cfg.Location).Format("2006-01-02"),
		"page": st.Newest.Page}))
	if st.Newer {
		b.WriteString("\n\n" + a.say(ctx, t, texts.UpdateStatusNext, texts.Vars{"newest": texts.Escape(st.Newest.Version.String())}))
	} else {
		b.WriteString("\n\n" + a.say(ctx, t, texts.UpdateStatusCurrent, nil))
	}
	if !st.Image {
		b.WriteString("\n\n" + a.say(ctx, t, texts.UpdateNoImage, nil))
	}
	return b.String()
}

// devBuild says a build without a release version moves to the image once,
// naming the image of the newest release when it is known.
func (a *Agent) devBuild(sp texts.Speaker, st update.Status) string {
	if st.Newest.Version.IsZero() {
		return sp.Text(texts.UpdateDevBuildAny, nil)
	}
	return sp.Text(texts.UpdateDevBuild, texts.Vars{varImage: update.ImageRef(st.Newest.Version)})
}

// refusal is the owner's words for an update that did not happen. The
// cause is logged; it is never shown, whatever the network said.
func (a *Agent) refusal(ctx context.Context, sp texts.Speaker, err error) string {
	if errors.Is(err, update.ErrDevBuild) {
		st, _ := a.d.Updater.Status(ctx) // the image of the newest release, when it can be had
		return a.devBuild(sp, st)
	}
	a.log.Warn("agent: /update refused", "reason", string(refusalKey(err)), "err", err)
	return sp.Text(refusalKey(err), nil)
}

// updateOutcome is what an install or a rollback did: the words for the
// owner, what the history says (after the caller's prefix) and whether the
// node changed and restarts.
type updateOutcome struct {
	Text string
	Note string
	Done bool
	Err  error // the refusal, when there was one
}

// installVersion installs version and, on success, restarts the node so the
// launcher runs it. The command and the Install button both run it.
func (a *Agent) installVersion(ctx context.Context, sp texts.Speaker, version, by string) updateOutcome {
	got, err := a.d.Updater.Install(ctx, version)
	if err != nil {
		return updateOutcome{Text: a.refusal(ctx, sp, err), Note: "was refused: " + string(refusalKey(err)), Err: err}
	}
	a.log.Info("agent: update installed, restarting", "release", got, "by", by)
	a.d.Restart()
	return updateOutcome{Text: sp.Text(texts.UpdateInstalled, texts.Vars{varVersion: texts.Escape(got)}), Note: "installed " + got, Done: true}
}

// rollbackVersion rolls back to the previous version and restarts the node.
// The command and the Rollback button both run it.
func (a *Agent) rollbackVersion(ctx context.Context, sp texts.Speaker, by string) updateOutcome {
	to, err := a.d.Updater.Rollback(ctx)
	if err != nil {
		return updateOutcome{Text: a.refusal(ctx, sp, err), Note: "was refused: " + string(refusalKey(err)), Err: err}
	}
	name := to
	if to == "" {
		name = sp.Text(texts.UpdateImageVersion, nil)
	}
	a.log.Info("agent: update rolled back, restarting", "to", to, "by", by)
	a.d.Restart()
	return updateOutcome{Text: sp.Text(texts.UpdateRolledBack, texts.Vars{varVersion: texts.Escape(name)}),
		Note: fmt.Sprintf("rolled back to %s", versionNoteName(to)), Done: true}
}

// updateInstall is /update install vX.Y.Z: it says it has started, installs,
// and on success restarts the node so the launcher runs the new version.
func (a *Agent) updateInstall(ctx context.Context, t *turn, version string) {
	if _, err := update.ParseVersion(version); err != nil {
		t.reply(a.say(ctx, t, texts.UpdateRefusedBadVersion, nil))
		return
	}
	a.updateEarly(ctx, t, a.say(ctx, t, texts.UpdateInstalling, texts.Vars{varVersion: texts.Escape(version)}))
	out := a.installVersion(ctx, a.speaker(ctx, t), version, t.msg.From)
	t.reply(out.Text)
	t.note(updateNote + out.Note)
}

// updateRollback is /update rollback.
func (a *Agent) updateRollback(ctx context.Context, t *turn) {
	out := a.rollbackVersion(ctx, a.speaker(ctx, t), t.msg.From)
	t.reply(out.Text)
	t.note(updateNote + out.Note)
}

// versionNoteName is the version a note names: "the image's version" for "".
func versionNoteName(v string) string {
	if v == "" {
		return "the version of the image"
	}
	return v
}

// updateEarly sends text now, ahead of the turn's reply, which a long install
// would otherwise hold back. A failure costs only the early word.
func (a *Agent) updateEarly(ctx context.Context, t *turn, text string) {
	o := event.Outgoing{IdemKey: t.event.IdemKey + updateEarlyKey, Transport: t.transport, Conv: t.replyConv, Text: text, ReplyTo: t.msg.Ref}
	if err := a.d.Outbox.Enqueue(ctx, o); err != nil {
		a.log.Warn("agent: /update: the early reply was not stored", "conv", t.event.Conv, "err", err)
	}
}
