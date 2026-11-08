package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/sandbox"
	"github.com/samber/do/v2"
)

// envDepth is how deep the sandbox looks for .env files under the home
// folder and a command's folder (the spike's note, M3d task 3.20).
const envDepth = 3

// Sandbox provides the command sandbox, checked once at start (R-CMD-3,
// ADR-0039). Off is not an error: the node runs, without commands, and
// says why.
func Sandbox() do.Provider[*sandbox.Sandbox] {
	return func(i do.Injector) (*sandbox.Sandbox, error) {
		b, start := do.MustInvoke[Bootstrap](i), time.Time(do.MustInvoke[ProcessStart](i))
		self, err := os.Executable()
		if err != nil {
			return nil, err
		}
		home, _ := os.UserHomeDir() // none: no home secrets to deny, and HOME=/
		node := sandbox.NodeFiles{DB: b.DB, LogDir: b.LogDir, ModulesDir: b.ModulesDir,
			Workspace: b.Agent.Workspace, Languages: b.Languages}
		p := sandbox.Policy{
			Deny:     sandbox.DenyList(home, node, os.Environ(), b.SandboxDeny),
			Env:      commandEnv(home, b.Agent.TZ),
			EnvRoots: roots(home),
			EnvDepth: envDepth,
		}
		s := sandbox.New(self, p, sandbox.HostKernel(), start)
		log := do.MustInvoke[*slog.Logger](i)
		if ok, r := s.Ready(); !ok {
			log.Info("sandbox: commands are off", "reason", r.String())
		} else {
			log.Info("sandbox: ready", "denied", len(p.Deny))
		}
		return s, nil
	}
}

// commandEnv is the whole environment a command gets (M3d design, section 5).
func commandEnv(home string, tz *time.Location) []string {
	if home == "" {
		home = "/"
	}
	zone := "UTC"
	if tz != nil {
		zone = tz.String()
	}
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + filepath.Clean(home),
		"LANG=C.UTF-8", "TZ=" + zone, "TERM=dumb"}
}

// roots is where .env files are looked for besides a command's folder.
func roots(home string) []string {
	if home == "" {
		return nil
	}
	return []string{home}
}
