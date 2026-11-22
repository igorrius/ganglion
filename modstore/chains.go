package modstore

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/links"
	"github.com/igorrius/ganglion/runner"
)

// ErrLoop is the refusal of a module whose cell triggers or links would
// close a loop with the active modules: one's report would start another
// that starts the first again (R-RUN-15, R-BUS-5).
var ErrLoop = errors.New("modstore: the triggers would form a loop")

// The node's reasons for a build that breaks the module graph; the detail
// names the loop, the chain or the links.
const (
	loopRefusal      = "the triggers would form a loop"
	linkLoopRefusal  = "the links would form a loop"
	deepRefusal      = "the build would make a chain of modules that is too deep"
	brokenLinkReason = "the build breaks a link"
)

// LinksFunc reads the links of the node's file as they are now
// (settings.LinksFile.Links, converted).
type LinksFunc func() ([]links.Link, error)

// NoLinks is the LinksFunc of a store whose node has none.
func NoLinks() ([]links.Link, error) { return nil, nil }

// UseLinks sets where the store reads the links its plan and install check
// a build against. It is called once, when the store is wired.
func (s *Store) UseLinks(f LinksFunc) { s.links = f }

// LinkModules are the active local modules with their ports and the cells
// they listen to, by short name. A version whose files cannot be read or
// parsed is left out: it does not run either.
func (s *Store) LinkModules(ctx context.Context) (map[string]links.Module, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT module, version, source, sha256, manifest_sha256, state, reason, installed_ms FROM module_versions
		WHERE source = ? AND state = 'active'`, SourceLocal)
	if err != nil {
		return nil, fmt.Errorf("modstore: %w", err)
	}
	vs, err := scan(rows)
	if err != nil {
		return nil, fmt.Errorf("modstore: %w", err)
	}
	out := map[string]links.Module{}
	for _, v := range vs {
		_, manifest, err := s.readLocal(v)
		if err != nil {
			continue
		}
		m, err := runner.ParseManifest(manifest)
		if err != nil {
			continue
		}
		out[ShortName(v.Module)] = links.FromManifest(m)
	}
	return out, nil
}

// checkLinks is the refusal and its detail when the build breaks the module
// graph: a link that touches the module no longer joins what it joined (a
// port gone, of another kind or another shape), or a loop or a chain deeper
// than links.MaxDepth runs through it. Only what the build makes new counts:
// a link already broken before it stays the owner's to mend. "" when none.
func (s *Store) checkLinks(ctx context.Context, module string, m runner.Manifest) (string, string, error) {
	ls, err := s.links()
	if err != nil {
		return "", "", fmt.Errorf("modstore: %w", err)
	}
	now, err := s.LinkModules(ctx)
	if err != nil {
		return "", "", err
	}
	short := ShortName(module)
	had := map[string]bool{}
	for _, p := range links.Check(now, ls) {
		had[p.Words()] = true
	}
	next := maps.Clone(now)
	next[short] = links.FromManifest(m)
	var broken []links.Problem
	for _, p := range links.Check(next, ls) {
		if p.Involves(short) && !had[p.Words()] {
			broken = append(broken, p)
		}
	}
	if len(broken) == 0 {
		return "", "", nil
	}
	return refusalOf(broken[0]), detailOf(short, broken), nil
}

// refusalOf is the node's reason for a problem of the graph.
func refusalOf(p links.Problem) string {
	switch p.Reason {
	case links.ReasonLoop:
		if p.Link == (links.Link{}) {
			return loopRefusal
		}
		return linkLoopRefusal
	case links.ReasonDeep:
		return deepRefusal
	case links.ReasonEnd, links.ReasonKind, links.ReasonShape:
	}
	return brokenLinkReason
}

// detailOf names the loop or the chain, or the links the build breaks. A
// loop is told from the module's side: it starts and ends at module.
func detailOf(module string, ps []links.Problem) string {
	switch ps[0].Reason {
	case links.ReasonLoop:
		return strings.Join(loopFrom(module, ps[0].Path), " → ")
	case links.ReasonDeep:
		return strings.Join(ps[0].Path, " → ")
	case links.ReasonEnd, links.ReasonKind, links.ReasonShape:
	}
	words := make([]string, 0, len(ps))
	for _, p := range ps {
		words = append(words, p.Words())
	}
	return strings.Join(words, "; ")
}

// loopFrom turns a loop (["a","b","a"]) to start and end at module when
// module is on it; another loop is returned as it is.
func loopFrom(module string, loop []string) []string {
	if len(loop) < 2 {
		return loop
	}
	ring := loop[:len(loop)-1]
	i := slices.Index(ring, module)
	if i <= 0 {
		return loop
	}
	out := append(slices.Clone(ring[i:]), ring[:i]...)
	return append(out, module)
}
