package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
	"github.com/igorrius/ganglion/update"
)

const (
	// updateButtonsTTL is how long the buttons of /update work.
	updateButtonsTTL = 24 * time.Hour
	// The history notes of what a button did.
	buttonInstalled  = "the owner installed %s by button"
	buttonRolledBack = "the owner rolled back to %s by button"
	buttonRefused    = "the owner pressed a button for %s and the update was refused: %s"
)

// updateButtons are the Install and Rollback buttons of the status st, made
// for the person who asked in their conversation; none when the node stores
// no buttons. A label holds the version and nothing else (R-UPD-7).
func (a *Agent) updateButtons(ctx context.Context, sp texts.Speaker, t *turn, st update.Status) ([]event.Button, error) {
	if a.d.Buttons == (NoButtons{}) {
		return nil, nil
	}
	var bs []pending.Button
	if st.Newer {
		v := st.Newest.Version.String()
		bs = append(bs, pending.Button{Action: update.ActionInstall, Label: sp.LabelWith(texts.UpdateButtonInstall, texts.Vars{varVersion: v}),
			Args: update.InstallArgs(v)})
	}
	if st.CanRollback {
		label := sp.Label(texts.UpdateButtonRollbackImg)
		if st.RollbackTo != "" {
			label = sp.LabelWith(texts.UpdateButtonRollback, texts.Vars{varVersion: st.RollbackTo})
		}
		bs = append(bs, pending.Button{Action: update.ActionRollback, Label: label, Args: update.RollbackArgs(st.RollbackTo)})
	}
	if len(bs) == 0 {
		return nil, nil
	}
	out := make([]event.Button, 0, len(bs))
	for i := range bs {
		bs[i].ID, bs[i].Principal, bs[i].Conv, bs[i].Expires = pending.NewButtonID(), t.msg.From, t.event.Conv, a.d.Now().Add(updateButtonsTTL)
		out = append(out, event.Button{ID: bs[i].ID, Label: bs[i].Label})
	}
	if err := a.d.Buttons.AddButtons(ctx, bs...); err != nil {
		return nil, err
	}
	return out, nil
}

// installButton installs the version of the button, as /update install does.
// A version that is already there installs nothing, so a press that runs
// again changes nothing more.
func (a *Agent) installButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	version, err := update.ParseInstallArgs(p.Button.Args)
	if err != nil {
		return pending.QuickEdit{}, fmt.Errorf("agent: button %s: arguments: %w", p.Button.ID, err)
	}
	out := a.installVersion(ctx, p.Speak, version, p.Button.Principal)
	switch {
	case out.Done:
		return pending.QuickEdit{Text: out.Text, Note: fmt.Sprintf(buttonInstalled, version)}, nil
	case errors.Is(out.Err, update.ErrNotNewer):
		return pending.QuickEdit{Text: p.Speak.Text(texts.UpdateAlreadyInstalled, texts.Vars{varVersion: texts.Escape(version)})}, nil
	}
	return pending.QuickEdit{Text: out.Text, Note: fmt.Sprintf(buttonRefused, version, out.Note)}, nil
}

// rollbackButton rolls back as /update rollback does, but only while the
// store's rollback target is still the one the button names: a button that
// was made before another update or rollback does nothing.
func (a *Agent) rollbackButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	to, err := update.ParseRollbackArgs(p.Button.Args)
	if err != nil {
		return pending.QuickEdit{}, fmt.Errorf("agent: button %s: arguments: %w", p.Button.ID, err)
	}
	if now, ok := a.d.Updater.RollbackTarget(); !ok || now != to {
		return pending.QuickEdit{Text: p.Speak.Text(texts.UpdateButtonStale, nil)}, nil
	}
	out := a.rollbackVersion(ctx, p.Speak, p.Button.Principal)
	if out.Done {
		return pending.QuickEdit{Text: out.Text, Note: fmt.Sprintf(buttonRolledBack, versionNoteName(to))}, nil
	}
	return pending.QuickEdit{Text: out.Text, Note: fmt.Sprintf(buttonRefused, versionNoteName(to), out.Note)}, nil
}
