package modstore

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/igorrius/ganglion/runner"
)

// ErrLoop is the refusal of a module whose cell triggers would close a loop
// with the active modules: one's report would start another that starts the
// first again (R-RUN-15).
var ErrLoop = errors.New("modstore: the triggers would form a loop")

// loopRefusal is the node's reason for a loop; the loop itself is the detail.
const loopRefusal = "the triggers would form a loop"

// loopOf returns the loop the module's listens would close among the active
// local modules (for example ["a","b","a"]), nil when none. Names are short
// names. Edges run from a cell's owner (the id's prefix) to each module that
// listens to it. others holds what the other modules listen to; the module's
// own active version is not among them.
func loopOf(module string, listens []string, others map[string][]string) []string {
	all := make(map[string][]string, len(others)+1)
	for name, l := range others {
		if name != module {
			all[name] = l
		}
	}
	all[module] = listens
	names := sortedKeys(all)
	next := func(owner string) []string {
		var out []string
		for _, n := range names {
			if slices.ContainsFunc(all[n], func(id string) bool { return strings.HasPrefix(id, owner+".") }) {
				out = append(out, n)
			}
		}
		return out
	}
	path, seen := []string{module}, map[string]bool{}
	var walk func(n string) bool
	walk = func(n string) bool {
		for _, m := range next(n) {
			if m == module {
				path = append(path, m)
				return true
			}
			if seen[m] {
				continue
			}
			seen[m] = true
			path = append(path, m)
			if walk(m) {
				return true
			}
			path = path[:len(path)-1]
		}
		return false
	}
	if walk(module) {
		return path
	}
	return nil
}

// otherListens is what every active local module but module listens to, by
// short name. A version whose files cannot be read or parsed listens to
// nothing: it does not run either.
func (s *Store) otherListens(ctx context.Context, module string) (map[string][]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT module, version, source, sha256, manifest_sha256, state, reason, installed_ms FROM module_versions
		WHERE source = ? AND state = 'active' AND module <> ?`, SourceLocal, module)
	if err != nil {
		return nil, fmt.Errorf("modstore: %w", err)
	}
	vs, err := scan(rows)
	if err != nil {
		return nil, fmt.Errorf("modstore: %w", err)
	}
	out := map[string][]string{}
	for _, v := range vs {
		_, manifest, err := s.readLocal(v)
		if err != nil {
			continue
		}
		m, err := runner.ParseManifest(manifest)
		if err != nil {
			continue
		}
		out[ShortName(v.Module)] = m.Listens()
	}
	return out, nil
}

// checkLoop is the refusal and its detail when the build's cell triggers
// would close a loop with the active local modules; "" when none.
func (s *Store) checkLoop(ctx context.Context, module string, m runner.Manifest) (string, string, error) {
	listens := m.Listens()
	if len(listens) == 0 {
		return "", "", nil
	}
	others, err := s.otherListens(ctx, module)
	if err != nil {
		return "", "", err
	}
	if loop := loopOf(ShortName(module), listens, others); loop != nil {
		return loopRefusal, strings.Join(loop, " → "), nil
	}
	return "", "", nil
}
