package agent

import (
	"context"
	"strings"

	"github.com/igorrius/ganglion/internal/transcript"
	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
	"github.com/igorrius/ganglion/turns"
)

// compactSplit returns where the kept part of msgs starts: keep the last
// keep messages, moved back so a tool result is never separated from the
// call that asked for it. 0 means there is nothing to compact.
func compactSplit(msgs []provider.Message, keep int) int {
	split := len(msgs) - keep
	if split <= 0 {
		return 0
	}
	for split > 0 && msgs[split].Role == provider.RoleTool {
		split--
	}
	return split
}

// compactInput is the text the compaction model folds: the earlier
// summary, if any, and the old messages as a transcript without tool
// calls.
func compactInput(summary string, old []provider.Message) string {
	var b strings.Builder
	if summary != "" {
		b.WriteString("Earlier summary:\n" + summary + "\n\nNew part of the conversation:\n")
	}
	for _, line := range transcript.Lines(old, transcript.WithoutToolCalls) {
		b.WriteString(line + "\n")
	}
	return b.String()
}

// compact folds all but the last KeepRecent messages into the summary with
// a cheap model. On failure the state is returned unchanged.
func (a *Agent) compact(ctx context.Context, st State, rec *turns.Turn) State {
	split := compactSplit(st.Messages, a.cfg.KeepRecent)
	if split == 0 {
		return st
	}
	msgs := []provider.Message{
		{Role: provider.RoleSystem, Content: compactPrompt},
		{Role: provider.RoleUser, Content: compactInput(st.Summary, st.Messages[:split])},
	}
	resp, ok := a.call(ctx, router.TaskReq{Tier: router.TierCheap, Kind: router.TaskCompaction}, msgs, nil, rec)
	if !ok || strings.TrimSpace(resp.Message.Content) == "" {
		a.log.Warn("agent: compaction failed; history kept", "conv", rec.Conv, "messages", len(st.Messages))
		return st
	}
	st.Summary = strings.TrimSpace(resp.Message.Content)
	st.Messages = append([]provider.Message(nil), st.Messages[split:]...)
	return st
}
