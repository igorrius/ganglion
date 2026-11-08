package builtin

import (
	"context"
	"encoding/json"

	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/tool"
)

// HostStatsTool is the name of host.stats.
const HostStatsTool = "host.stats"

// StatsReader reads the machine's and the node's numbers (host.Stats).
type StatsReader interface {
	Read(ctx context.Context) host.Report
}

// HostStats is host.stats (R-CMD-5): memory, load, uptime, disk and the
// node's own files, read in Go with no subprocess, so it works where
// host.exec is off. The numbers and the node's own paths are not outside
// content, so it does not taint, and it sends nothing off the node but its
// answer, like metrics.show.
type HostStats struct{ s StatsReader }

// NewHostStats returns host.stats over s.
func NewHostStats(s StatsReader) *HostStats { return &HostStats{s: s} }

// Spec implements tool.Handler.
func (*HostStats) Spec() tool.Spec {
	return tool.Spec{Name: HostStatsTool,
		Description: "This machine's memory, load and uptime, free disk space, the node's own memory, and the sizes of the node's own files. Needs no command.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Class:       tool.ClassRead, Policy: tool.PolicyAuto, AdminOnly: true, LocalSafe: true}
}

// Run implements tool.Handler.
func (h *HostStats) Run(ctx context.Context, _ tool.Call) (tool.Result, error) {
	return tool.Result{Text: host.Render(h.s.Read(ctx))}, nil
}
