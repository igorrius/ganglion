package mcpadmin

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/internal/text"
	"github.com/igorrius/ganglion/mcpclient"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/status"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/pending"
)

// stateLocked is a server that signs in while the secret store is locked:
// it cannot reach its credential (R-CFG-10). The hub does not know it, so
// it is a state of this package's views only.
const stateLocked mcpclient.State = "store_locked"

// view is one server as /mcp and /status show it.
type view struct {
	name     string
	state    mcpclient.State
	tools    int
	signedIn time.Time
	was      string // the line's digest, for Disconnect
}

// views are the servers of the file, by name, with the hub's states.
func (a *Admin) views(ctx context.Context) ([]view, error) {
	m, err := a.d.File.MCP()
	if err != nil {
		return nil, fmt.Errorf("mcpadmin: read the mcp section: %w", err)
	}
	states := map[string]mcpclient.Status{}
	for _, s := range a.d.Hub.States() {
		states[s.Name] = s
	}
	locked := a.d.Locked()
	out := make([]view, 0, len(m.Servers))
	for _, name := range slices.Sorted(maps.Keys(m.Servers)) {
		line := m.Servers[name]
		v := view{name: name, state: mcpclient.StateConnecting, was: digest(line, true)}
		if st, ok := states[name]; ok {
			v.state, v.tools = st.State, st.Tools
		}
		if locked && line.Auth != settings.MCPAuthNone {
			v.state = stateLocked
		}
		if line.Auth == settings.MCPAuthOAuth && !locked {
			if v.signedIn, err = a.d.SignIns.SignedIn(ctx, name); err != nil {
				return nil, err
			}
		}
		out = append(out, v)
	}
	return out, nil
}

// stateKeys are the words of each state in /mcp.
var stateKeys = map[mcpclient.State]texts.Key{
	mcpclient.StateConnecting:   texts.MCPStateConnecting,
	mcpclient.StateConnected:    texts.MCPStateConnected,
	mcpclient.StateUnreachable:  texts.MCPStateUnreachable,
	mcpclient.StateToolsChanged: texts.MCPStateToolsChanged,
	mcpclient.StateUnauthorized: texts.MCPStateUnauthorized,
	stateLocked:                 texts.MCPStateLocked,
}

// Card is /mcp (R-MCP-10): each server with its state, the time of its
// last sign-in in loc and its number of tools, and a Disconnect button
// for each, stored for principal in conv.
func (a *Admin) Card(ctx context.Context, sp texts.Speaker, principal, conv string, loc *time.Location) (string, []event.Button, error) {
	vs, err := a.views(ctx)
	if err != nil {
		return "", nil, err
	}
	if len(vs) == 0 {
		return sp.Text(texts.MCPListNone, nil), nil, nil
	}
	lines := []string{sp.Text(texts.MCPListHead, nil)}
	stored := make([]pending.Button, 0, len(vs))
	for _, v := range vs {
		row := sp.Text(texts.MCPListRow, texts.Vars{varServer: text.CodeSpan(v.name), varState: sp.Text(stateKeys[v.state], nil),
			varN: fmt.Sprint(v.tools)})
		if !v.signedIn.IsZero() {
			row += sp.Text(texts.MCPListSignedIn, texts.Vars{varTime: timeIn(v.signedIn, loc)})
		}
		lines = append(lines, "- "+row)
		args, err := json.Marshal(approval{Server: v.name, Was: v.was})
		if err != nil {
			return "", nil, fmt.Errorf("mcpadmin: /mcp: %w", err)
		}
		stored = append(stored, pending.Button{ID: pending.NewButtonID(), Action: ActionDisconnect,
			Label:     markDisconnect + sp.LabelWith(texts.ButtonMCPDisconnect, texts.Vars{varServer: v.name}),
			Principal: principal, Conv: conv, Args: args, Expires: a.now().Add(buttonsTTL)})
	}
	if err := a.d.Buttons.AddButtons(ctx, stored...); err != nil {
		return "", nil, fmt.Errorf("mcpadmin: /mcp: %w", err)
	}
	buttons := make([]event.Button, len(stored))
	for i, b := range stored {
		buttons[i] = event.Button{ID: b.ID, Label: b.Label}
	}
	return strings.Join(lines, "\n"), buttons, nil
}

// disconnectByButton removes the server's line, its registration, tokens
// and key, and stops it (R-MCP-5, R-MCP-10). A Disconnect on a card older
// than the line's last change changes nothing; one whose server is gone
// already (a rerun) removes what is left and answers the same.
func (a *Admin) disconnectByButton(ctx context.Context, p pending.QuickPress) (pending.QuickEdit, error) {
	ap, err := a.pressed(ctx, p)
	if err != nil {
		return pending.QuickEdit{}, err
	}
	a.changeMu.Lock()
	defer a.changeMu.Unlock()
	gone, err := a.current(ap.Server, "")
	if err != nil {
		return pending.QuickEdit{}, err
	}
	if ok, err := a.current(ap.Server, ap.Was); err != nil || !ok && !gone {
		return stale(p), err
	}
	if err := a.d.File.RemoveServer(ap.Server); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("mcpadmin: disconnect %s: %w", ap.Server, err)
	}
	if err := a.Apply(); err != nil {
		return pending.QuickEdit{}, err
	}
	if err := a.d.SignIns.Disconnect(ctx, ap.Server); err != nil {
		return pending.QuickEdit{}, fmt.Errorf("mcpadmin: disconnect %s: %w", ap.Server, err)
	}
	a.in.change(ctx, changeDisconnect)
	return pending.QuickEdit{
		Text: p.Speak.Text(texts.MCPDisconnected, texts.Vars{varServer: text.CodeSpan(ap.Server)}),
		Note: "the owner disconnected the MCP server " + ap.Server + " by button with /mcp; its tools are gone",
	}, nil
}

// statusKeys are the /status line of each state that is not connected.
var statusKeys = map[mcpclient.State]texts.Key{
	mcpclient.StateConnecting:   texts.StatusMCPConnecting,
	mcpclient.StateUnreachable:  texts.StatusMCPUnreachable,
	mcpclient.StateToolsChanged: texts.StatusMCPToolsChanged,
	mcpclient.StateUnauthorized: texts.StatusMCPUnauthorized,
	stateLocked:                 texts.StatusMCPLocked,
}

// Name implements status.Source.
func (*Admin) Name() string { return "mcp" }

// Status implements status.Source: a line for each server that is not
// connected (R-MCP-10).
func (a *Admin) Status(ctx context.Context, _ status.Scope) ([]status.Check, error) {
	vs, err := a.views(ctx)
	if err != nil {
		return nil, err
	}
	var out []status.Check
	for _, v := range vs {
		key, ok := statusKeys[v.state]
		if !ok {
			continue
		}
		state := status.Warn
		if v.state == stateLocked || v.state == mcpclient.StateUnauthorized {
			state = status.Fail
		}
		out = append(out, status.Check{Section: status.SectionProblems, State: state, Line: key,
			Vars: texts.Vars{varServer: text.CodeSpan(v.name)}})
	}
	return out, nil
}
