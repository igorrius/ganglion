package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/samber/do/v2"
)

// leftoverAge is how long the node's temporary leftovers stay (R-CMD-8).
const leftoverAge = 24 * time.Hour

// tempPrefixes are the names the node gives its temporary folders.
var tempPrefixes = []string{"ganglion-claude-", "ganglion-mcp-", "ganglion-skill-", "ganglion-sandbox-"}

// Places provides the node's places on disk, the one list host.stats and the
// janitor read.
func Places() do.Provider[[]host.Place] {
	return func(i do.Injector) ([]host.Place, error) {
		b := do.MustInvoke[Bootstrap](i)
		ps := []host.Place{
			{Name: host.PlaceDatabase, Path: filepath.Dir(b.DB), Prefix: filepath.Base(b.DB)},
			{Name: host.PlaceWorkspace, Path: b.Agent.Workspace},
			{Name: host.PlaceModules, Path: b.ModulesDir},
			{Name: host.PlaceLanguages, Path: b.Languages},
		}
		if b.LogDir != "" {
			ps = append(ps, host.Place{Name: host.PlaceLogs, Path: b.LogDir})
		}
		if b.ClaudeDir != "" {
			ps = append(ps, host.Place{Name: host.PlaceClaude, Path: b.ClaudeDir, MaxAge: leftoverAge})
		}
		for _, p := range tempPrefixes {
			ps = append(ps, host.Place{Name: host.PlaceTemp, Path: os.TempDir(), Prefix: p, MaxAge: leftoverAge})
		}
		return ps, nil
	}
}

// Janitor provides the cleanup of the node's places.
func Janitor() do.Provider[*host.Janitor] {
	return func(i do.Injector) (*host.Janitor, error) {
		return host.NewJanitor(do.MustInvoke[[]host.Place](i), do.MustInvoke[*slog.Logger](i)), nil
	}
}

// Stats provides host.stats' reader over the node's places.
func Stats() do.Provider[*host.Stats] {
	return func(i do.Injector) (*host.Stats, error) {
		b := do.MustInvoke[Bootstrap](i)
		return host.NewStats(do.MustInvoke[[]host.Place](i), filepath.Dir(b.DB)), nil
	}
}

// commandLimits are the runner's limits. Until long commands resume their
// conversation (M3d task 7), a call waits for its command to the end.
func commandLimits() host.Limits {
	lim := host.DefaultLimits()
	lim.Wait = lim.Max
	return lim
}

// Runner provides the runner of host commands, in the sandbox.
func Runner() do.Provider[*host.Runner] {
	return func(i do.Injector) (*host.Runner, error) {
		b := do.MustInvoke[Bootstrap](i)
		values := redact.Values(b.SecretValues()...)
		return host.NewRunner(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, do.MustInvoke[*sandbox.Sandbox](i),
			do.MustInvoke[*sqlqueue.Queue](i).Put, commandLimits(),
			host.WithRedact(func(s string) string { return values(redact.String(s)) }),
			host.WithRunnerLog(do.MustInvoke[*slog.Logger](i)))
	}
}
