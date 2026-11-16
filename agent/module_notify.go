package agent

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/turns"
)

// moduleNotifyEvent sends a module's message, or a node line about a
// module, to the admin it is for, with no model (R-RUN-16, ADR-0066,
// ruling 1 of M4c). Only the node's events count, and only for an admin:
// one from another source, with a node line the agent does not know, or
// for a principal who is no longer an admin is logged and sends nothing.
func (a *Agent) moduleNotifyEvent(ctx context.Context, t *turn) error {
	t.record.Outcome = turns.OutcomeCommand
	p := t.notify
	if t.event.Source != event.SourceModule || !knownNode(p.Node) {
		a.log.Warn("agent: a module message that is not the node's; dropped", "source", t.event.Source, "conv", t.event.Conv)
		return nil
	}
	t.transport, t.record.Principal = p.Transport, p.Principal
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if !t.admin {
		a.log.Warn("agent: a module message for a principal who is not an admin; dropped", "module", p.Module, "principal", p.Principal)
		return nil
	}
	if p.Node == event.NotifyText && p.To == "" { // an idle session rolls over first, as for a reminder's note
		if _, err := a.idle(ctx, t); err != nil {
			return err
		}
	}
	a.moduleNotify(ctx, t, p)
	return nil
}

// knownNode reports whether n is one of the node's lines, or the module's
// text.
func knownNode(n event.NotifyNode) bool {
	switch n {
	case event.NotifyText, event.NotifyLimitLine, event.NotifyChainLine:
		return true
	}
	return false
}

// moduleNotify writes the node note into history and returns the message. A message for
// another destination than the owner's chat (R-RUN-18) writes no note and leaves that
// conversation's session as it is: "notified you" is the owner's note, and a group's
// history is not the place for it. The module's text
// goes only into the outgoing message, escaped and signed with the module's name; the model never sees it (R-RUN-13).
// A node line writes no note. The note does not move the session's Last (noteQuiet), so a
// module writing all day never keeps a session alive. The message carries a tainted origin that names the module
// (ruling 2 of M4c, R-DLV-15): a reply to it is told the module and never its text; stamp keeps that origin.
func (a *Agent) moduleNotify(ctx context.Context, t *turn, p event.ModuleNotify) {
	if p.Node == event.NotifyText && p.To == "" {
		t.noteQuiet(fmt.Sprintf("%s%s notified you at %s", moduleNotePrefix, moduleName(p.Module), moduleClock(p.At, a.cfg.Location)))
	}
	run := p.Run
	if p.Node != event.NotifyText { // a node line is the node's: no run sent it
		run = ""
	}
	t.out = append(t.out, event.Outgoing{
		IdemKey: t.event.IdemKey + ":out", Transport: p.Transport, Conv: t.event.Conv,
		Text:      a.notifyText(ctx, t, p),
		Signature: notifySignature(p),
		NoPreview: true,
		Origin:    &event.Origin{Kind: event.OriginModule, Module: p.Module, Run: run, Tainted: true},
	})
}

// notifySignature is the module's short name and running version ("demo-alerts
// v2") under its own text, as the node's signature (ADR-0074); a node line is
// the node's and has none. A version that is not a number is left out.
func notifySignature(p event.ModuleNotify) string {
	if p.Node != event.NotifyText {
		return ""
	}
	if n, err := strconv.Atoi(p.Version); err != nil || n < 1 || strconv.Itoa(n) != p.Version {
		return moduleName(p.Module)
	}
	return moduleName(p.Module) + " v" + p.Version
}

// notifyText is the message: the module's text, escaped so the module cannot format,
// with its name apart from it in the signature, or the node's line in the owner's
// language.
func (a *Agent) notifyText(ctx context.Context, t *turn, p event.ModuleNotify) string {
	name := texts.Escape(moduleName(p.Module))
	vars := texts.Vars{varModule: name, "n": strconv.Itoa(p.PerDay)}
	switch p.Node {
	case event.NotifyLimitLine:
		return a.say(ctx, t, texts.ModuleNotifyLimit, vars)
	case event.NotifyChainLine:
		return a.say(ctx, t, texts.ModuleChainStopped, vars)
	case event.NotifyText:
	}
	return texts.Escape(oneLine(p.Text))
}

// lineBreaks folds every kind of line break into one space.
var lineBreaks = strings.NewReplacer("\r", " ", "\n", " ", "\u0085", " ", "\u2028", " ", "\u2029", " ", "\v", " ", "\f", " ")

// oneLine is s as a single line, so a module's text cannot end with a line
// that looks like the node's signature.
func oneLine(s string) string { return lineBreaks.Replace(s) }
