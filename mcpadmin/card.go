package mcpadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// Actions of the cards' buttons.
const (
	ActionConnect    = "mcp.card.connect"
	ActionAskAll     = "mcp.card.ask_all"
	ActionCancel     = "mcp.card.cancel"
	ActionDisconnect = "mcp.card.disconnect"
)

// Symbols on the buttons, outside the translated label (R-LANG-6).
const (
	markConnect    = "✅ "
	markAskAll     = "❓ "
	markCancel     = "✖️ "
	markDisconnect = "🔌 "
)

// Bounds of a card.
const (
	// buttonsTTL is how long a card's buttons can be pressed.
	buttonsTTL = 24 * time.Hour
	// maxCardRunes is how long a tools card may grow; the tools past it
	// are counted, not listed. Telegram takes 4096.
	maxCardRunes = 3500
	// aboutRunes is how much of a tool's description the card shows.
	aboutRunes = 80
)

// approval is what Connect writes: the server's line as the card
// proposed it. It is the buttons' arguments, fixed when the card was made,
// so a press approves exactly what the admin read.
type approval struct {
	Server      string                        `json:"server"`
	URL         string                        `json:"url"`
	Auth        settings.MCPAuth              `json:"auth"`
	Header      string                        `json:"header,omitempty"`
	Timeout     time.Duration                 `json:"timeout"`
	Tools       map[string]settings.MCPPolicy `json:"tools"`
	Fingerprint string                        `json:"fingerprint"`
}

// toolsCard lists the server's tools and makes the card that approves
// them (R-MCP-3): each tool's name and the first line of its description
// as data, a proposed policy, and Connect, Ask for all and Cancel. A tool
// the server marks read-only is proposed as auto, any other as confirm; a
// server connected before keeps the policies the admin chose. n is the
// number of tools taken.
func (a *Admin) toolsCard(ctx context.Context, sp texts.Speaker, w who, t target, auth settings.MCPAuth,
	c *mcpclient.Client, key string,
) (out event.Outgoing, n int, err error) {
	l, err := c.ListTools(ctx)
	if err != nil {
		return event.Outgoing{}, 0, err
	}
	old, _, err := a.line(t.name)
	if err != nil {
		return event.Outgoing{}, 0, err
	}
	ap := approval{Server: t.name, URL: t.url, Auth: auth, Header: t.header, Timeout: time.Duration(old.Timeout),
		Tools: proposed(l.Tools, old.Tools), Fingerprint: l.Fingerprint}
	if ap.Timeout == 0 {
		ap.Timeout = settings.DefaultMCPTimeout
	}
	buttons, err := a.cardButtons(ctx, sp, w, ap)
	if err != nil {
		return event.Outgoing{}, 0, err
	}
	return event.Outgoing{IdemKey: key, Transport: w.transport, Conv: w.conv, Text: cardText(sp, t, l, ap.Tools),
		Buttons: buttons}, len(l.Tools), nil
}

// proposed is each tool's policy on the card: the admin's earlier choice
// for a tool they approved before, else auto for a read-only tool and
// confirm for the rest.
func proposed(tools []mcpclient.Tool, before map[string]settings.MCPPolicy) map[string]settings.MCPPolicy {
	out := make(map[string]settings.MCPPolicy, len(tools))
	for _, t := range tools {
		switch p, ok := before[t.Short]; {
		case ok:
			out[t.Short] = p
		case t.ReadOnly:
			out[t.Short] = settings.MCPPolicyAuto
		default:
			out[t.Short] = settings.MCPPolicyConfirm
		}
	}
	return out
}

// cardText is the tools card: the head, the tools by policy, the skipped
// ones, cut to maxCardRunes. Names and descriptions are the server's text,
// shown as data.
func cardText(sp texts.Speaker, t target, l mcpclient.ToolList, policies map[string]settings.MCPPolicy) string {
	w := &cardWriter{sp: sp, left: len(l.Tools) + len(l.Skipped)}
	w.b.WriteString(sp.Text(texts.AskMCPToolsCard, texts.Vars{varServer: text.CodeSpan(t.name), varHost: texts.Escape(t.host()),
		varN: fmt.Sprint(len(l.Tools))}))
	if len(l.Tools) == 0 {
		w.b.WriteString("\n\n" + sp.Text(texts.AskMCPToolsNone, nil))
	}
	for _, group := range []struct {
		policy settings.MCPPolicy
		head   texts.Key
	}{
		{settings.MCPPolicyAuto, texts.AskMCPToolsAuto},
		{settings.MCPPolicyConfirm, texts.AskMCPToolsConfirm},
		{settings.MCPPolicyForbidden, texts.AskMCPToolsForbidden},
	} {
		var lines []string
		for _, tl := range l.Tools {
			if policies[tl.Short] == group.policy {
				lines = append(lines, toolLine(tl))
			}
		}
		w.section(sp.Text(group.head, nil), lines)
	}
	skipped := make([]string, len(l.Skipped))
	for i, s := range l.Skipped {
		skipped[i] = "- " + text.CodeSpan(s.Name) + " (" + string(s.Reason) + ")"
	}
	w.section(sp.Text(texts.AskMCPToolsSkipped, nil), skipped)
	return w.b.String()
}

// toolLine is one tool on the card: its name and the first line of its
// description, both escaped.
func toolLine(t mcpclient.Tool) string {
	about := t.Description
	if about == "" {
		about = t.Title
	}
	line, _, _ := strings.Cut(strings.TrimSpace(about), "\n")
	out := "- " + text.CodeSpan(t.Short)
	if line = strings.TrimSpace(line); line != "" {
		out += " — " + texts.Escape(text.Truncate(line, aboutRunes))
	}
	return out
}

// cardWriter writes a card's sections while it has room.
type cardWriter struct {
	b    strings.Builder
	sp   texts.Speaker
	left int  // lines not written yet
	full bool // the card ran out of room and says how many are left
}

// section writes a head and its lines; when the card runs out of room it
// says how many lines are left and writes nothing more.
func (w *cardWriter) section(head string, lines []string) {
	if len(lines) == 0 || w.full {
		return
	}
	w.b.WriteString("\n\n" + head)
	for _, l := range lines {
		if utf8.RuneCountInString(w.b.String())+utf8.RuneCountInString(l) > maxCardRunes {
			w.b.WriteString("\n" + w.sp.Text(texts.AskMCPToolsMore, texts.Vars{varN: fmt.Sprint(w.left)}))
			w.full = true
			return
		}
		w.b.WriteString("\n" + l)
		w.left--
	}
}

// cardButtons stores Connect, Ask for all and Cancel for the admin.
func (a *Admin) cardButtons(ctx context.Context, sp texts.Speaker, w who, ap approval) ([]event.Button, error) {
	args, err := json.Marshal(ap)
	if err != nil {
		return nil, fmt.Errorf("mcpadmin: the card's buttons: %w", err)
	}
	expires := a.now().Add(buttonsTTL)
	stored := []pending.Button{
		{ID: pending.NewButtonID(), Action: ActionConnect, Label: markConnect + sp.Label(texts.ButtonMCPConnect)},
		{ID: pending.NewButtonID(), Action: ActionAskAll, Label: markAskAll + sp.Label(texts.ButtonMCPAskAll)},
		{ID: pending.NewButtonID(), Action: ActionCancel, Label: markCancel + sp.Label(texts.ButtonMCPCancel)},
	}
	out := make([]event.Button, len(stored))
	for i := range stored {
		stored[i].Principal, stored[i].Conv, stored[i].Args, stored[i].Expires = w.principal, w.conv, args, expires
		out[i] = event.Button{ID: stored[i].ID, Label: stored[i].Label}
	}
	if err := a.d.Buttons.AddButtons(ctx, stored...); err != nil {
		return nil, fmt.Errorf("mcpadmin: the card's buttons: %w", err)
	}
	return out, nil
}

// QuickActions are the cards' buttons by action name, for the agent
// (agent.Deps.QuickActions).
func (a *Admin) QuickActions() map[string]pending.QuickAction {
	return map[string]pending.QuickAction{ActionConnect: a.connectByButton, ActionAskAll: a.askAllByButton,
		ActionCancel: a.cancelByButton, ActionDisconnect: a.disconnectByButton}
}

// connectByButton writes the server's line as the card proposed it and
// applies it: the tools are offered at once (R-MCP-3, R-CFG-13). A rerun
// writes the same line.
func (a *Admin) connectByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	return a.approve(ctx, p, false)
}

// askAllByButton is Connect with every tool that is on offer set to ask.
func (a *Admin) askAllByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	return a.approve(ctx, p, true)
}

// approve writes and applies the line of the press.
func (a *Admin) approve(ctx context.Context, p pending.QuickPress, askAll bool) (pending.QuickEdit, error) {
	ap, err := a.pressed(ctx, p)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	if askAll {
		for t, pol := range ap.Tools {
			if pol != settings.MCPPolicyForbidden {
				ap.Tools[t] = settings.MCPPolicyConfirm
			}
		}
	}
	_, existed, err := a.line(ap.Server)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	why := "connected"
	if existed {
		why = "approved again"
	}
	line := settings.MCPServer{URL: ap.URL, Auth: ap.Auth, Header: ap.Header, Timeout: settings.Duration(ap.Timeout),
		Tools: ap.Tools, Fingerprint: ap.Fingerprint}
	if err := a.d.File.SetServer(ap.Server, line, why); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("mcpadmin: connect %s: %w", ap.Server, err)
	}
	if err := a.Apply(); err != nil {
		return pending.QuickEdit{}, err
	}
	a.in.connect(ctx, outcomeConnected)
	auto, ask := counts(ap.Tools)
	return pending.QuickEdit{
		Text: p.Speak.Text(texts.MCPConnected, texts.Vars{varServer: text.CodeSpan(ap.Server), varN: fmt.Sprint(auto + ask),
			"auto": fmt.Sprint(auto), "ask": fmt.Sprint(ask)}),
		Note: fmt.Sprintf("the owner connected the MCP server %s by button: %d tools, %d run by themselves, %d ask first; "+
			"mcp.tools lists them and mcp.use turns them on", ap.Server, auto+ask, auto, ask),
	}, nil
}

// counts is how many tools run by themselves and how many ask.
func counts(tools map[string]settings.MCPPolicy) (auto, ask int) {
	for _, p := range tools {
		switch p {
		case settings.MCPPolicyAuto:
			auto++
		case settings.MCPPolicyConfirm:
			ask++
		case settings.MCPPolicyForbidden:
		}
	}
	return auto, ask
}

// cancelByButton drops the sign-in of a server that is not connected: its
// registration and tokens (R-MCP-3). A server connected before stays as
// it was.
func (a *Admin) cancelByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	ap, err := a.pressed(ctx, p)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	_, existed, err := a.line(ap.Server)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	if !existed {
		if err := a.d.SignIns.Disconnect(ctx, ap.Server); err != nil {
			return pending.QuickEdit{}, fmt.Errorf("mcpadmin: cancel %s: %w", ap.Server, err)
		}
	}
	a.in.connect(ctx, outcomeCancelled)
	return pending.QuickEdit{
		Text: p.Speak.Text(texts.MCPCancelled, texts.Vars{varServer: text.CodeSpan(ap.Server)}),
		Note: "the owner pressed Cancel: the MCP server " + ap.Server + " was not connected",
	}, nil
}

// pressed checks who pressed and reads the card's approval.
func (a *Admin) pressed(ctx context.Context, p pending.QuickPress) (approval, error) {
	if err := a.admin(ctx, p.Button.Principal); err != nil {
		return approval{}, err
	}
	var ap approval
	if err := json.Unmarshal(p.Button.Args, &ap); err != nil {
		return approval{}, fmt.Errorf("mcpadmin: button arguments: %w", err)
	}
	if !settings.MCPNameRE.MatchString(ap.Server) {
		return approval{}, fmt.Errorf("mcpadmin: button arguments: a bad server name")
	}
	if ap.Tools == nil {
		ap.Tools = map[string]settings.MCPPolicy{}
	}
	return ap, nil
}
