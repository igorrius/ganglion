package modhost

import (
	"context"

	"github.com/igorrius/ganglion/event"
	"github.com/igorrius/ganglion/links"
)

// This file is what the cell triggers (R-RUN-15, cellevents.go) and the
// links share: the depth cap and the node's line for a chain that stopped.

// maxDepth is the longest chain of modules a run starts: a run at this
// depth that writes cells, emits or calls starts nobody (M4c design section
// 4).
const maxDepth = links.MaxDepth

// cellInput is the payload of a cell trigger's run: the cells it listens
// to that changed, and who wrote them. The module reads the values itself.
type cellInput struct {
	Cells    []string `json:"cells"`
	Producer string   `json:"producer"`
}

// chainLine says in the log, with msg, and to l's owner, that a chain
// through l stopped, once per active build, in memory (ruling 6 of M4c):
// after a restart the log may say it once more, while the line's key keeps
// the owner's line to one as long as the inbox holds it. With loops refused
// at install, this is a backstop.
func (h *Host) chainLine(ctx context.Context, l *loaded, msg string) {
	if !h.firstStop(l.sha) {
		return
	}
	h.d.Log.Warn("modhost: "+msg, "module", l.name, "version", l.version, "sha", l.sha)
	line := event.ModuleNotify{Node: event.NotifyChainLine, PerDay: perDay(l)}
	if err := h.putNotify(ctx, l, "notify:"+l.name+":chain:"+l.sha, line, h.d.Now()); err != nil {
		h.d.Log.Error("modhost: the line of a stopped chain was not put", "module", l.name, "err", err)
	}
}

// firstStop reports whether the build sha's chain stops for the first time
// since the node started, and remembers it.
func (h *Host) firstStop(sha string) bool {
	h.stateMu.Lock()
	defer h.stateMu.Unlock()
	if h.stops[sha] {
		return false
	}
	h.stops[sha] = true
	return true
}
