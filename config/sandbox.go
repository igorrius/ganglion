package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/igorrius/ganglion/sandbox"
	"github.com/igorrius/ganglion/update"
	"github.com/samber/do/v2"
)

// selfImage is the running binary as the kernel holds it, whatever the
// file at its path has become since.
const selfImage = "/proc/self/exe"

// envDepth is how deep the sandbox looks for .env files under the home
// folder and a command's folder (the spike's note, M3d task 3.20).
const envDepth = 3

// Sandbox provides the command sandbox, checked once at start (R-CMD-3,
// ADR-0039). Off is not an error: the node runs, without commands, and
// says why.
func Sandbox() do.Provider[*sandbox.Sandbox] {
	return func(i do.Injector) (*sandbox.Sandbox, error) {
		b, start := do.MustInvoke[Bootstrap](i), time.Time(do.MustInvoke[ProcessStart](i))
		binary, err := os.Executable()
		if err != nil {
			return nil, err
		}
		cwd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		home, _ := os.UserHomeDir() // none: no home secrets to deny, and HOME=/
		releases, _ := update.DirFor(b.DB)
		node := sandbox.NodeFiles{Binary: binary, DB: b.DB, Settings: settingsFile(b), LogDir: b.LogDir, ModulesDir: b.ModulesDir, ClaudeDir: b.ClaudeDir,
			Workspace: b.Agent.Workspace, Languages: b.Languages, MasterKey: b.MasterKey, Releases: releases}
		environ := os.Environ()
		p := sandbox.Policy{
			Deny:      func() []string { return sandbox.DenyList(home, cwd, node, environ, b.SandboxDeny) },
			Env:       commandEnv(home, b.Agent.TZ),
			EnvRoots:  roots(home),
			EnvDepth:  envDepth,
			HideNames: b.SandboxHideNames,
		}
		// The helper is this process's own image, not the path it came
		// from: a command that replaced the file would otherwise run as the
		// next helper, before any rule.
		s := sandbox.New(selfImage, p, sandbox.HostKernel(), start)
		log := do.MustInvoke[*slog.Logger](i)
		if ok, r := s.Ready(); !ok {
			log.Info("sandbox: commands are off", "reason", r.String())
		} else {
			log.Info("sandbox: ready", "denied", len(p.Deny()), "hide_names", p.HideNames)
		}
		return s, nil
	}
}

// commandEnv is the whole environment a command gets (M3d design, section 5).
// GOTOOLCHAIN=local keeps go to the installed toolchain: a go.mod in the
// command's folder could otherwise make even go version download and run
// another one.
func commandEnv(home string, tz *time.Location) []string {
	if home == "" {
		home = "/"
	}
	zone := "UTC"
	if tz != nil {
		zone = tz.String()
	}
	return []string{"PATH=/usr/local/bin:/usr/bin:/bin", "HOME=" + filepath.Clean(home),
		"LANG=C.UTF-8", "TZ=" + zone, "TERM=dumb", "GOTOOLCHAIN=local"}
}

// roots is where .env files are looked for besides a command's folder.
func roots(home string) []string {
	if home == "" {
		return nil
	}
	return []string{home}
}
