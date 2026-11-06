package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool/builtin"
)

const (
	// metricsNote is the history's note that /metrics was shown.
	metricsNote    = commandPrefix + "the owner looked at the agent's metrics with /metrics"
	replyNoMetrics = "The metrics cannot be drawn right now; the node's log says why."
)

// showMetrics is /metrics: metrics.show through the tool chain, as the
// turn's call, for an admin, without a model (ruling M2).
func (a *Agent) showMetrics(ctx context.Context, t *turn, arg string) error {
	if err := a.setAdmin(ctx, t); err != nil {
		return err
	}
	if _, ok := a.d.Tools.Lookup(builtin.MetricsTool); !ok || !t.admin {
		t.reply(a.say(ctx, t, texts.ReplyNotAvailable, nil))
		return nil
	}
	shown := len(t.out)
	args, _ := json.Marshal(map[string]string{"choice": strings.TrimSpace(arg)}) // a map of strings always marshals
	_, err := a.useTool(ctx, t, provider.ToolCall{Name: builtin.MetricsTool, Args: args}, false)
	if err != nil {
		a.log.Warn("agent: /metrics failed", "err", err) // journalled too; the turn itself succeeds
	}
	if err != nil || len(t.out) == shown {
		t.reply(replyNoMetrics)
		return nil
	}
	t.note(metricsNote)
	return nil
}
