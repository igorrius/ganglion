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
// values come from cfg over the manifest's defaults. A module with a cell
// trigger and no network right also gets one run on the node's current
// cells, which are only read.
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
	m.ConfigValues = configValues(m, cfg)
	short := modstore.ShortName(b.Module)
	ex, err := modtrial.Parse(examples, m, short)
	if err != nil {
		if errors.Is(err, modtrial.ErrExamples) {
			return modtrial.Report{Refused: err.Error()}, nil
		}
		return modtrial.Report{}, err
	}
	// The node's settings sit under an example's own config values.
	for i := range ex.Examples {
		e := &ex.Examples[i]
		if e.Config == nil {
			e.Config = map[string]string{}
		}
		for k, v := range m.ConfigValues {
			if _, ok := e.Config[k]; !ok {
				e.Config[k] = v
			}
		}
	}
	g := h.newGiven(b.Module, m)
	o := modtrial.Options{Start: h.d.Now(), Log: h.moduleLog(g)}
	if len(m.Listens()) > 0 && len(m.Capabilities.HTTPFetch) == 0 {
		o.Current = h.currentCells(ctx, m)
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
