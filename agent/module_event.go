package agent

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/turns"
)

// moduleNotePrefix starts the user-role note of a module's escalation:
// "[module] deye-reader: auth_failed (version 3, 14:30). <detail>".
const moduleNotePrefix = "[module] "

// localPrefix starts the full name of a module built on this node; the
// note names the module without it, as its cells and tools do.
const localPrefix = "local/"

// maxModuleDetail caps the node's description in the note.
const maxModuleDetail = 300

// moduleKindRE is the shape of the node's kinds (auth_failed,
// module.crashloop): a payload with anything else is not the node's.
var moduleKindRE = regexp.MustCompile(`^[a-z][a-z_.]{0,39}$`)

// moduleEvent makes a module's escalation a turn of the admin it is for
// (design section 8.4, ruling 10 of the M4b plan): the node's note of it
// joins the history as the user's line and the model answers the owner,
// with its tools. The note is the node's own text, not outside content: the
// host words the kind and the detail itself (R-RUN-14). Only the node's
// module events count, and only for an admin; over the daily limit the
// note joins the history without a model turn.
func (a *Agent) moduleEvent(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeCommand
	m := t.module
	if t.event.Source != event.SourceModule || !moduleKindRE.MatchString(m.Kind) {
		a.log.Warn("agent: a module event that is not the node's", "source", t.event.Source, "conv", t.event.Conv)
		return nil
	}
	t.transport, t.record.Principal = m.Transport, m.Principal
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		a.log.Warn("agent: a module event for a principal who is not an admin; skipped", "module", m.Module, "principal", m.Principal)
		return nil
	}
	if _, err := a.idle(ctx, t); err != nil {
		return err
	}
	note := provider.Message{Role: provider.RoleUser, Content: moduleNote(m, a.cfg.Location)}
	if _, over := a.overLimit(ctx, t.now, t.msg.From, t.event.ID); over {
		t.state.Messages, t.state.Last = append(t.state.Messages, note), t.now
		return nil
	}
	t.record.Outcome = turns.OutcomeOK
	if done, err := a.converse(ctx, t, note); err != nil || done {
		return err
	}
	_, err := a.compactHistory(ctx, t)
	return err
}

// moduleNote is the history's line for a module's escalation, with its time
// in the node's zone and the detail on one line, capped.
func moduleNote(m event.Module, loc *time.Location) string {
	name := strings.TrimPrefix(m.Module, localPrefix)
	at := time.UnixMilli(m.At).In(loc).Format("15:04")
	detail := text.Truncate(strings.Join(strings.Fields(m.Detail), " "), maxModuleDetail)
	return fmt.Sprintf("%s%s: %s (version %s, %s). %s", moduleNotePrefix, name, m.Kind, m.Version, at, detail)
}
