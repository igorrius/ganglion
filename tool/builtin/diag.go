package builtin

import (
	"context"
	"errors"

	"github.com/igorrius/ganglion/alert"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/tool"
)

// Diagnostics answers the diagnostic tools (package diag, R-OBS-7).
type Diagnostics interface {
	Tool(name string) (provider.Tool, bool)
	Exec(ctx context.Context, call provider.ToolCall) (string, error)
}

// diagNames are the diagnostic tools the agent gets: the searches. The
// settings and the queues stay with `ganglion mcp`.
var diagNames = []string{"logs.search", "journal.search", "turns.failed"}

// errNotPrivate refuses a diagnostic search outside the admin's private
// chat with the bot.
var errNotPrivate = errors.New("diagnostics read every conversation, so they work only in your private chat with the bot")

// NewDiagTools gives the agent the diagnostic searches, for admins only
// and only in the caller's private chat: they read every conversation,
// so an answer in a group could pass another chat's text on to it.
// Logs and the journal quote fetched pages and remote errors, so every
// result is outside content: wrapped as data, and it taints the session.
func NewDiagTools(d Diagnostics, private alert.PrivateConvFunc) []tool.Handler {
	out := make([]tool.Handler, 0, len(diagNames))
	for _, name := range diagNames {
		if spec, ok := d.Tool(name); ok {
			out = append(out, &diagTool{d: d, spec: spec, private: private})
		}
	}
	return out
}

type diagTool struct {
	d       Diagnostics
	spec    provider.Tool
	private alert.PrivateConvFunc
}

func (t *diagTool) Spec() tool.Spec {
	return tool.Spec{Name: t.spec.Name, Description: t.spec.Description, InputSchema: t.spec.InputSchema,
		Class: tool.ClassExternal, Policy: tool.PolicyAuto, AdminOnly: true, LocalSafe: true}
}

func (t *diagTool) Run(ctx context.Context, c tool.Call) (tool.Result, error) {
	if _, conv, ok := t.private(c.Principal); !ok || conv != c.Conv {
		return tool.Result{}, errNotPrivate
	}
	text, err := t.d.Exec(ctx, provider.ToolCall{ID: c.Key(), Name: c.Name, Args: c.Args})
	if err != nil {
		return tool.Result{}, err
	}
	return tool.Result{Text: text, External: true}, nil
}
