package mcpadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// Was pins the server's line when the card was made (digest); a press
	// changes nothing once the line is another, so an old card never
	// brings back a disconnected server or older rules.
	Was string `json:"was"`
}

// toolsCard lists the server's tools and makes the card that approves
// them (R-MCP-3): each tool's name and the first line of its description
// as data, a proposed policy (proposed), and Connect, Ask for all and
// Cancel. n is the number of tools taken.
func (a *Admin) toolsCard(ctx context.Context, sp texts.Speaker, w who, t target, auth settings.MCPAuth,
	c *mcpclient.Client, key string,
) (out event.Outgoing, n int, err error) {
	l, err := c.ListTools(ctx)
	if err != nil {
		return event.Outgoing{}, 0, err
	}
	old, existed, err := a.line(t.name)
	if err != nil {
		return event.Outgoing{}, 0, err
	}
	policies, err := proposed(t, l.Tools, old.Tools)
	if err != nil {
		return event.Outgoing{}, 0, err
	}
	ap := approval{Server: t.name, URL: t.url, Auth: auth, Header: t.header, Timeout: time.Duration(old.Timeout),
		Tools: policies, Fingerprint: l.Fingerprint, Was: digest(old, existed)}
	if ap.Timeout == 0 {
		ap.Timeout = settings.DefaultMCPTimeout
	}
	body, err := cardText(sp, t, l, ap.Tools)
	if err != nil {
		return event.Outgoing{}, 0, err
	}
	buttons, err := a.cardButtons(ctx, sp, w, ap)
	if err != nil {
		return event.Outgoing{}, 0, err
	}
	return event.Outgoing{IdemKey: key, Transport: w.transport, Conv: w.conv, Text: body, Buttons: buttons}, len(l.Tools), nil
}

// unknownToolsError refuses a proposal that names tools the server's list
// does not have (R-MCP-3). Its text is for the model: the names are its own
// arguments, cut to a few.
type unknownToolsError struct{ names []string }

func (e *unknownToolsError) Error() string {
	shown := e.names
	if len(shown) > maxNamedUnknown {
		shown = shown[:maxNamedUnknown]
	}
	for i, n := range shown {
		shown[i] = text.Truncate(n, 64)
	}
	more := ""
	if len(e.names) > len(shown) {
		more = fmt.Sprintf(" and %d more", len(e.names)-len(shown))
	}
	return "the server has no tool named " + strings.Join(shown, ", ") + more + "; no card was sent. " +
		"mcp.tools lists a connected server's tools; call mcp.connect again with the names as the server lists them"
}

// maxNamedUnknown is how many unknown names an error names.
const maxNamedUnknown = 5

// errCardTooLong is a proposal whose tools that run by themselves, and are
// not marked read-only, do not fit one message even by name: the card would
// have to hide some, so none is made.
var errCardTooLong = errors.New("too many tools would run by themselves without being read-only to show them on one card; " +
	"narrow the proposal (make more tools confirm or forbidden) and call mcp.connect again")

// proposed is each tool's policy on the card, the first that applies: the
// owner's rules, the owner's others, the admin's earlier choice for a tool
// they approved before, else auto for a read-only tool and confirm for the
// rest. A name in rules that no tool has is an error naming it.
func proposed(t target, tools []mcpclient.Tool, before map[string]settings.MCPPolicy) (map[string]settings.MCPPolicy, error) {
	byName := make(map[string]string, 3*len(tools))
	for _, tl := range tools {
		for _, n := range []string{tl.Short, tl.Own, tl.Name} {
			byName[n] = tl.Short
		}
	}
	rules := make(map[string]settings.MCPPolicy, len(t.rules))
	var unknown []string
	for name, p := range t.rules {
		short, ok := byName[name]
		if !ok {
			unknown = append(unknown, name)
			continue
		}
		rules[short] = p
	}
	if len(unknown) > 0 {
		slices.Sort(unknown)
		return nil, &unknownToolsError{names: unknown}
	}
	out := make(map[string]settings.MCPPolicy, len(tools))
	for _, tl := range tools {
		switch p, ok := before[tl.Short]; {
		case rules[tl.Short] != "":
			out[tl.Short] = rules[tl.Short]
		case t.others != "":
			out[tl.Short] = t.others
		case ok:
			out[tl.Short] = p
		case tl.ReadOnly:
			out[tl.Short] = settings.MCPPolicyAuto
		default:
			out[tl.Short] = settings.MCPPolicyConfirm
		}
	}
	return out, nil
}

// cardText is the tools card: the head, then the tools by policy, the
// skipped ones, cut to maxCardRunes. The tools that run by themselves
// without the server marking them read-only come first and are never cut:
// when their lines with descriptions do not fit they are written by name
// alone, and when the names do not fit either there is no card
// (errCardTooLong). Names and descriptions are the server's text, shown as
// data.
func cardText(sp texts.Speaker, t target, l mcpclient.ToolList, policies map[string]settings.MCPPolicy) (string, error) {
	w := &cardWriter{sp: sp, left: len(l.Tools) + len(l.Skipped)}
	w.b.WriteString(sp.Text(texts.AskMCPToolsCard, texts.Vars{varServer: text.CodeSpan(t.name), varHost: texts.Escape(t.host()),
		varN: fmt.Sprint(len(l.Tools))}))
	if len(l.Tools) == 0 {
		w.b.WriteString("\n\n" + sp.Text(texts.AskMCPToolsNone, nil))
	}
	by := func(policy settings.MCPPolicy, readOnly *bool) []mcpclient.Tool {
		var out []mcpclient.Tool
		for _, tl := range l.Tools {
			if policies[tl.Short] == policy && (readOnly == nil || tl.ReadOnly == *readOnly) {
				out = append(out, tl)
			}
		}
		return out
	}
	yes, no := true, false
	if err := w.mandatory(sp.Text(texts.AskMCPToolsAutoWrite, nil), by(settings.MCPPolicyAuto, &no)); err != nil {
		return "", err
	}
	w.section(sp.Text(texts.AskMCPToolsAuto, nil), toolLines(by(settings.MCPPolicyAuto, &yes)))
	w.section(sp.Text(texts.AskMCPToolsConfirm, nil), toolLines(by(settings.MCPPolicyConfirm, nil)))
	w.forbidden(by(settings.MCPPolicyForbidden, nil))
	skipped := make([]string, len(l.Skipped))
	for i, s := range l.Skipped {
		skipped[i] = "- " + text.CodeSpan(s.Name) + " (" + string(s.Reason) + ")"
	}
	w.section(sp.Text(texts.AskMCPToolsSkipped, nil), skipped)
	return w.b.String(), nil
}

// toolLines is a tool's line each.
func toolLines(tools []mcpclient.Tool) []string {
	out := make([]string, len(tools))
	for i, tl := range tools {
		out[i] = toolLine(tl)
	}
	return out
}

// namesLine is a tool on the card by its name alone.
func namesLine(t mcpclient.Tool) string { return "- " + text.CodeSpan(t.Short) }

// maxForbiddenNamed is how many forbidden tools the card names; with more it
// gives the count.
const maxForbiddenNamed = 5

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

// mandatory writes the lines that must not be hidden: with descriptions
// when they fit, by name when they do not, else errCardTooLong.
func (w *cardWriter) mandatory(head string, tools []mcpclient.Tool) error {
	if len(tools) == 0 {
		return nil
	}
	used := utf8.RuneCountInString(w.b.String()) + utf8.RuneCountInString(head) + 2
	build := func(line func(mcpclient.Tool) string) (string, int) {
		var b strings.Builder
		for _, tl := range tools {
			b.WriteString("\n" + line(tl))
		}
		return b.String(), utf8.RuneCountInString(b.String())
	}
	body, n := build(toolLine)
	if used+n > maxCardRunes {
		body, n = build(namesLine)
	}
	if used+n > maxCardRunes {
		return errCardTooLong
	}
	w.b.WriteString("\n\n" + head + body)
	w.left -= len(tools)
	return nil
}

// forbidden writes the forbidden tools: their names when few, else a count.
// It is short and is written even when the card has run out of room.
func (w *cardWriter) forbidden(tools []mcpclient.Tool) {
	if len(tools) == 0 {
		return
	}
	w.left -= len(tools)
	if len(tools) > maxForbiddenNamed {
		w.b.WriteString("\n\n" + w.sp.Text(texts.AskMCPToolsForbiddenCount, texts.Vars{varN: fmt.Sprint(len(tools))}))
		return
	}
	w.b.WriteString("\n\n" + w.sp.Text(texts.AskMCPToolsForbidden, nil))
	for _, tl := range tools {
		w.b.WriteString("\n" + namesLine(tl))
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
// applies it: the tools are offered at once (R-MCP-3, R-CFG-13). A press
// on a card made before the line last changed changes nothing; a rerun of
// the press finds its own line and answers the same.
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
	line := settings.MCPServer{URL: ap.URL, Auth: ap.Auth, Header: ap.Header, Timeout: settings.Duration(ap.Timeout),
		Tools: ap.Tools, Fingerprint: ap.Fingerprint}
	a.changeMu.Lock()
	defer a.changeMu.Unlock()
	written, err := a.current(ap.Server, digest(line, true))
	if err != nil {
		return pending.QuickEdit{}, err
	}
	if !written { // a rerun of this press finds its own line and answers the same
		if ok, err := a.current(ap.Server, ap.Was); err != nil || !ok {
			return stale(p), err
		}
		why := "connected"
		if ap.Was != "" {
			why = "approved again"
		}
		if err := a.d.File.SetServer(ap.Server, line, why); err != nil {
			return pending.QuickEdit{}, fmt.Errorf("mcpadmin: connect %s: %w", ap.Server, err)
		}
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
	a.changeMu.Lock()
	defer a.changeMu.Unlock()
	if ok, err := a.current(ap.Server, ap.Was); err != nil || !ok {
		return stale(p), err
	}
	if ap.Was == "" {
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
