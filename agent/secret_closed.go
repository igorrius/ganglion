package agent

import (
	"context"
	"fmt"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/turns"
)

// secretNotePrefix starts the node note of a secret request that closed.
const secretNotePrefix = "[secret] "

// secretClosed writes the node note of a secret request that closed into
// the session of the conversation that asked, with no model (R-INT-6). The
// card was folded by the node already. Only the node's event counts, and
// only for an admin. The note names the secret and its scope and says how
// the request ended; it never holds a value, which no event carries.
func (a *Agent) secretClosed(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeCommand
	s := t.secret
	if t.event.Source != event.SourceIntake {
		a.log.Warn("agent: a secret note that is not the node's; dropped", "source", t.event.Source, "conv", t.event.Conv)
		return nil
	}
	t.transport, t.record.Principal = s.Transport, s.Principal
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		a.log.Warn("agent: a secret note for a principal who is not an admin; dropped", "request", s.Request)
		return nil
	}
	if _, err := a.idle(ctx, t); err != nil { // an idle session rolls over first, as for a module's note
		return err
	}
	t.noteQuiet(secretNote(s, moduleClock(s.At.UnixMilli(), a.cfg.Location)))
	return nil
}

// secretNote is the note's text for the model.
func secretNote(s event.SecretClosed, at string) string {
	switch s.Outcome {
	case event.SecretSet:
		return fmt.Sprintf("%s%s in scope %s was set by the owner at %s; its value is in the store and is never shown to you",
			secretNotePrefix, s.Name, s.Scope, at)
	case event.SecretExpired:
		return fmt.Sprintf("%sthe request for %s in scope %s expired at %s unanswered; nothing was set", secretNotePrefix, s.Name, s.Scope, at)
	case event.SecretFailed:
	}
	return fmt.Sprintf("%s%s in scope %s was not set at %s: the pasted line was not accepted or the store refused it",
		secretNotePrefix, s.Name, s.Scope, at)
}
