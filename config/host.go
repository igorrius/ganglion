package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/cells"
	"github.com/igorrius/ganglion/host"
	"github.com/igorrius/ganglion/internal/redact"
	"github.com/igorrius/ganglion/queue/sqlqueue"
	"github.com/igorrius/ganglion/sandbox"
	"github.com/igorrius/ganglion/tool/builtin"
	"github.com/samber/do/v2"
)

// leftoverAge is how long the node's temporary leftovers stay (R-CMD-8).
const leftoverAge = 24 * time.Hour

// tempPrefixes are the names the node gives its temporary folders.
var tempPrefixes = []string{"ganglion-claude-", "ganglion-mcp-", "ganglion-skill-", "ganglion-sandbox-", "ganglion-build-"}

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
		ps = append(ps, host.Place{Name: host.PlaceBuilds, Path: buildsDir(b), MaxAge: leftoverAge})
		if root := buildConfig(b).TempRoot; root != "" {
			ps = append(ps, host.Place{Name: host.PlaceTemp, Path: root, Prefix: "ganglion-build-", MaxAge: leftoverAge})
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

// Runner provides the runner of host commands, in the sandbox.
func Runner() do.Provider[*host.Runner] {
	return func(i do.Injector) (*host.Runner, error) { return newRunner(i, host.DefaultLimits()) }
}

// newRunner builds the runner of host commands with lim and opts.
func newRunner(i do.Injector, lim host.Limits, opts ...host.RunnerOption) (*host.Runner, error) {
	b := do.MustInvoke[Bootstrap](i)
	opts = append([]host.RunnerOption{
		host.WithRedact(secretRedact(b)),
		host.WithRunnerLog(do.MustInvoke[*slog.Logger](i)),
	}, opts...)
	return host.NewRunner(do.MustInvoke[Startup](i), do.MustInvoke[Database](i).DB, do.MustInvoke[*sandbox.Sandbox](i),
		do.MustInvoke[*sqlqueue.Queue](i).Put, lim, opts...)
}

// Switch provides the kill switch of host.exec, its cell defined at start.
func Switch() do.Provider[*host.Switch] {
	return func(i do.Injector) (*host.Switch, error) {
		return host.NewSwitch(do.MustInvoke[Startup](i), do.MustInvoke[*cells.Service](i))
	}
}

// sandboxGate is open while the sandbox is on. Skill scripts run behind it
// alone: the kill switch stops host.exec, not skills.
type sandboxGate struct{ s *sandbox.Sandbox }

func (g sandboxGate) Open() bool { ok, _ := g.s.Ready(); return ok }

// commandGate is open while the sandbox is on and the kill switch is not off.
type commandGate struct {
	sandboxGate
	sw *host.Switch
}

func (g commandGate) Open() bool { return g.sandboxGate.Open() && g.sw.Open() }

// secretRedact removes secrets by pattern and the node's own secret values,
// for command output and the alert that names a command.
func secretRedact(b Bootstrap) func(string) string {
	values := redact.Values(b.SecretValues()...)
	return func(s string) string { return values(redact.String(s)) }
}

// commandHome is the node user's home: the folder of a host command without
// dir, for host.exec, the policy, the agent and the plans alike.
func commandHome() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "/"
	}
	return home
}

// HostExec provides host.exec over the runner, in the node user's home.
func HostExec() do.Provider[*builtin.HostExec] {
	return func(i do.Injector) (*builtin.HostExec, error) {
		s := do.MustInvoke[*sandbox.Sandbox](i)
		return builtin.NewHostExec(do.MustInvoke[*host.Runner](i), host.NewIgnored(s), commandGate{sandboxGate{s}, do.MustInvoke[*host.Switch](i)}, commandHome()), nil
	}
}
