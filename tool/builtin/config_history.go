package builtin

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/confighist"
	"github.com/igorrius/ganglion/settings"
	"github.com/igorrius/ganglion/texts"
	"github.com/igorrius/ganglion/tool"
	"go.yaml.in/yaml/v3"
)

// Bounds of config.history and config.rollback.
const (
	defaultHistory = 10
	maxHistory     = 20
	// rollbackSearch is how many changes back the latest change of an
	// allowlisted field is looked for.
	rollbackSearch = 20
	// maxDiffLines bounds one change's diff in config.history.
	maxDiffLines = 40
	shortID      = 8
)

// historyArgs are config.history's arguments.
type historyArgs struct {
	Limit int `json:"limit"`
}

// history answers config.history. The node's own file is not outside
// content: each line in it passed an owner's press or was written on the
// host.
func (g Configuring) history(ctx context.Context, c tool.Call) (tool.Result, error) {
	var a historyArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return tool.Result{}, fmt.Errorf("arguments: %w", err)
	}
	n := a.Limit
	if n < 1 || n > maxHistory {
		n = defaultHistory
	}
	cs, err := g.History.Changes(ctx, n)
	if errors.Is(err, confighist.ErrNoHistory) {
		return tool.Result{Text: "This node keeps no history of its file: git is not installed."}, nil
	}
	if err != nil {
		return tool.Result{}, err
	}
	if len(cs) == 0 {
		return tool.Result{Text: "No change of the node's file is recorded yet."}, nil
	}
	var b strings.Builder
	for _, ch := range cs {
		d, err := g.History.Diff(ctx, ch.ID)
		if err != nil {
			return tool.Result{}, err
		}
		fmt.Fprintf(&b, "%s %s %s: %s\n%s\n", ch.ID[:min(shortID, len(ch.ID))], ch.When.In(g.Now().Location()).Format("2006-01-02 15:04"),
			ch.Source, ch.Reason, firstLines(d, maxDiffLines))
	}
	return tool.Result{Text: b.String()}, nil
}

// firstLines is s cut to n lines, saying how many were left out.
func firstLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "\n")
	}
	return strings.Join(lines[:n], "\n") + fmt.Sprintf("\n… %d more lines", len(lines)-n)
}

// rollbackArgs are config.rollback's arguments.
type rollbackArgs struct {
	Change string `json:"change"`
}

// rollbackPlan is what a rollback proposes and what it leaves.
type rollbackPlan struct {
	proposal
	// kept are the fields the change touched that are not rolled back
	// here, with where they are changed.
	kept []string
}

// errNothingToRollBack is a change that touched no allowlisted field.
var errNothingToRollBack = errors.New("nothing to roll back here")

// rollbackQuestion is the card of config.rollback.
func (g Configuring) rollbackQuestion(ctx context.Context, c tool.Call) (tool.Result, error) {
	r, err := g.rollbackOf(ctx, c)
	if err != nil {
		return tool.Result{}, err
	}
	old, err := g.check(r.changes)
	if err != nil {
		return tool.Result{}, err
	}
	extra := ""
	if len(r.kept) > 0 {
		extra = c.Speak.Text(texts.AskConfigKept, texts.Vars{"fields": texts.Escape(strings.Join(r.kept, ", "))})
	}
	return tool.Result{Text: changeQuestion(c, r.proposal, old, extra)}, nil
}

// rollback writes an approved config.rollback.
func (g Configuring) rollback(ctx context.Context, c tool.Call) (tool.Result, error) {
	r, err := g.rollbackOf(ctx, c)
	if err != nil {
		return tool.Result{}, err
	}
	res, err := g.write(ctx, c, r.proposal, confighist.SourceRollback)
	if err != nil {
		return tool.Result{}, err
	}
	if len(r.kept) > 0 {
		res.Text += "Not rolled back here: " + strings.Join(r.kept, ", ") + "\n"
	}
	return res, nil
}

// rollbackOf finds the change to roll back and the earlier values of the
// allowlisted fields it changed (R-CFG-16).
func (g Configuring) rollbackOf(ctx context.Context, c tool.Call) (rollbackPlan, error) {
	var a rollbackArgs
	if err := strictJSON(c.Args, &a); err != nil {
		return rollbackPlan{}, fmt.Errorf("arguments: %w", err)
	}
	if a.Change != "" {
		return g.planFor(ctx, a.Change)
	}
	cs, err := g.History.Changes(ctx, rollbackSearch)
	if err != nil {
		return rollbackPlan{}, err
	}
	for _, ch := range cs {
		r, err := g.planFor(ctx, ch.ID)
		if errors.Is(err, errNothingToRollBack) {
			continue
		}
		return r, err
	}
	return rollbackPlan{}, fmt.Errorf("%w: none of the latest %d changes touched a field config.propose changes", errNothingToRollBack, len(cs))
}

// planFor is the rollback of one change.
func (g Configuring) planFor(ctx context.Context, id string) (rollbackPlan, error) {
	cs, err := g.History.Changes(ctx, maxScan)
	if err != nil {
		return rollbackPlan{}, err
	}
	i := slices.IndexFunc(cs, func(ch confighist.Change) bool {
		return ch.ID == id || (len(id) >= 7 && strings.HasPrefix(ch.ID, id))
	})
	if i < 0 {
		return rollbackPlan{}, fmt.Errorf("%w: %q", confighist.ErrNoChange, id)
	}
	ch := cs[i]
	before, after, err := g.History.Versions(ctx, ch.ID)
	if err != nil {
		return rollbackPlan{}, err
	}
	changes, err := allowedChanges(before, after)
	if err != nil {
		return rollbackPlan{}, err
	}
	kept, err := otherChanges(before, after, changes)
	if err != nil {
		return rollbackPlan{}, err
	}
	if len(changes) == 0 {
		return rollbackPlan{}, fmt.Errorf("%w: change %s touched only %s", errNothingToRollBack, ch.ID[:min(shortID, len(ch.ID))], strings.Join(kept, ", "))
	}
	reason := fmt.Sprintf("roll back %s: %s", ch.ID[:min(shortID, len(ch.ID))], ch.Reason)
	return rollbackPlan{proposal{changes: changes, reason: oneLineOf(reason, maxReasonRunes)}, kept}, nil
}

// maxScan is how far back an id named for a rollback is looked up.
const maxScan = 500

// oneLineOf is s on one line, at most n runes.
func oneLineOf(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// allowedChanges are the allowlisted fields that differ between before and
// after, set back to their values in before (nil where before had none).
func allowedChanges(before, after []byte) ([]settings.Change, error) {
	vb, err := settings.Values(before, settings.AllowedPaths)
	if err != nil {
		return nil, err
	}
	va, err := settings.Values(after, settings.AllowedPaths)
	if err != nil {
		return nil, err
	}
	var cs []settings.Change
	for _, p := range settings.AllowedPaths {
		b, bok := vb[p]
		a, aok := va[p]
		if !sameValue(b, bok, a, aok) {
			cs = append(cs, settings.Change{Path: p, Value: b})
		}
	}
	return cs, nil
}

// otherChanges are the fields, two levels deep, that still differ once the
// allowlisted ones are set back: what the rollback leaves, each with where
// it is changed.
func otherChanges(before, after []byte, back []settings.Change) ([]string, error) {
	rolled, err := settings.Apply(after, back)
	if err != nil {
		return nil, err
	}
	b, err := twoLevels(before)
	if err != nil {
		return nil, err
	}
	a, err := twoLevels(rolled)
	if err != nil {
		return nil, err
	}
	keys := slices.Sorted(maps.Keys(b))
	for k := range a {
		if _, ok := b[k]; !ok {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	var out []string
	for _, k := range keys {
		bv, bok := b[k]
		av, aok := a[k]
		if !sameValue(bv, bok, av, aok) {
			out = append(out, k+" ("+string(settings.Where(k))+")")
		}
	}
	return out, nil
}

// twoLevels is the file's values by "section" or "section.field".
func twoLevels(src []byte) (map[string]any, error) {
	var top map[string]any
	if err := yaml.Unmarshal(src, &top); err != nil {
		return nil, fmt.Errorf("%s: %w", settings.FileName, err)
	}
	out := map[string]any{}
	for k, v := range top {
		m, ok := v.(map[string]any)
		if !ok {
			out[k] = v
			continue
		}
		for f, fv := range m {
			out[k+"."+f] = fv
		}
	}
	return out, nil
}
