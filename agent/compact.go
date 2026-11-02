package agent

import (
	"context"
	"strings"

	"github.com/igorrius/ganglion/provider"
	"github.com/igorrius/ganglion/router"
)

// compactSplit returns where the kept part of msgs starts: keep the last
// keep messages, moved back so a tool result is never separated from the
// call that asked for it. 0 means there is nothing to compact.
func compactSplit(msgs []provider.Message, keep int) int {
	split := len(msgs) - keep
	if split <= 0 {
		return 0
	}
	for split > 0 && msgs[split].Role == "tool" {
		split--
	}
	return split
}

// compact folds all but the last KeepRecent messages into the summary with
// a cheap model. On failure the state is returned unchanged.
func (a *Agent) compact(ctx context.Context, st State, tr *turnRecord) State {
	split := compactSplit(st.Messages, a.cfg.KeepRecent)
	if split == 0 {
		return st
	}
	var b strings.Builder
	if st.Summary != "" {
		b.WriteString("Earlier summary:\n" + st.Summary + "\n\nNew part of the conversation:\n")
	}
	for _, m := range st.Messages[:split] {
		switch {
		case m.Role == "user":
			b.WriteString("User: " + m.Content + "\n")
		case m.Role == "assistant" && m.Content != "":
			b.WriteString("Assistant: " + m.Content + "\n")
		case m.Role == "tool":
			b.WriteString("Tool result: " + m.Content + "\n")
		}
	}
	msgs := []provider.Message{{Role: "system", Content: compactPrompt}, {Role: "user", Content: b.String()}}
	resp, ok := a.call(ctx, router.TaskReq{Tier: "cheap", Kind: "compaction"}, msgs, nil, tr)
	if !ok || strings.TrimSpace(resp.Message.Content) == "" {
		a.log.Warn("agent: compaction failed; history kept", "conv", tr.Conv, "messages", len(st.Messages))
		return st
	}
	st.Summary = strings.TrimSpace(resp.Message.Content)
	st.Messages = append([]provider.Message(nil), st.Messages[split:]...)
	return st
}
