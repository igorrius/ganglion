package modhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/modstore"
	"github.com/igorrius/ganglion/modtrial"
	"github.com/igorrius/ganglion/runner"
)

// Trial runs build b on its examples (the bytes of its examples.yaml; nil
// when the source has none) in a sandbox that shares nothing with the node:
// no cell is written, no state kept, no message counted or sent. Config
// values of an example are the manifest's defaults overlaid by the
// example's own config only: the node's settings (cfg) do not apply to it,
// so an example is deterministic and its stored report holds after the
// settings change. A module with a cell trigger and no network right also
// gets one run on the node's current cells, which are only read; that run
// uses cfg, since it shows what the module does with the owner's settings.
//
// A refused examples file is a report with Refused set, not an error. An
// error names what failed in the node's words (ErrCheckFailed); the
// module's own text goes to the log only.
func (h *Host) Trial(ctx context.Context, b modstore.Build, examples []byte, cfg map[string]any) (modtrial.Report, error) {
	if examples == nil {
		return modtrial.Report{Passed: true}, nil
	}
	m, err := runner.ParseManifest(b.Manifest)
	if err != nil {
		h.d.Log.Warn("modhost: a build's manifest does not load", "module", b.Module, "err", err)
		return modtrial.Report{}, fmt.Errorf("%w: its manifest does not load", ErrCheckFailed)
	}
	short := modstore.ShortName(b.Module)
	ex, err := modtrial.Parse(examples, m, short)
	if err != nil {
		if errors.Is(err, modtrial.ErrExamples) {
			return modtrial.Report{Refused: err.Error()}, nil
		}
		return modtrial.Report{}, err
	}
	// A trial reads no secret of the node's: the examples bring fake ones,
	// so the logger hides nothing and no secret store is touched.
	g := &given{byName: map[string][]string{}, hide: func(s string) string { return s }}
	o := modtrial.Options{Start: h.d.Now(), Log: h.moduleLog(g).With("trial", true)}
	if len(m.Listens()) > 0 && len(m.Capabilities.HTTPFetch) == 0 {
		o.Current, o.NowConfig = h.currentCells(ctx, m), configValues(m, cfg)
	}
	rep, err := modtrial.Run(ctx, b.Wasm, m, short, ex, o)
	if err != nil {
		h.d.Log.Warn("modhost: a build does not load for its trial", "module", b.Module, "err", g.logged(err.Error()))
		return modtrial.Report{}, fmt.Errorf("%w: it does not load on this node", ErrCheckFailed)
	}
	return rep, nil
}

// currentCells reads the node's real cells that m may read, with their
// values and change times; a cell the node lacks, or one with no value, is
// left out.
func (h *Host) currentCells(ctx context.Context, m runner.Manifest) map[string]modtrial.Seeded {
	out := map[string]modtrial.Seeded{}
	for _, id := range m.Cells.Read {
		c, err := h.d.Cells.Get(ctx, id)
		if errors.Is(err, cells.ErrNotFound) || (err == nil && len(c.Reported) == 0) {
			continue
		}
		if err != nil {
			h.d.Log.Warn("modhost: a cell cannot be read for a trial", "cell", id, "err", err)
			continue
		}
		out[id] = modtrial.Seeded{Value: json.RawMessage(c.Reported), At: c.Updated}
	}
	return out
}
