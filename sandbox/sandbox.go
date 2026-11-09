// Package sandbox runs host commands and skill scripts so that they cannot
// gain privileges, see the node's variables, or read or write the node's data
// and secret files and the owner's secret stores (R-CMD-3, ADR-0039).
package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
)

// HelperArg is the argument that makes the ganglion binary the sandbox's
// helper: `ganglion __sandbox`, with the spec on file descriptor 3.
const HelperArg = "__sandbox"

// exitSetup is the helper's exit code when it could not set up the
// sandbox or start the command: the command never ran.
const exitSetup = 125

// exitReadable is the helper's exit code when the self-test could read
// the file it should not, or open a Unix socket (spec.Try).
const exitReadable = 126

// specFD is the file descriptor the helper reads its spec from.
const specFD = 3

// spec is what the node hands the helper.
type spec struct {
	Argv   []string
	Env    []string // the whole environment of the command
	Dir    string   // its working folder
	Grants []Grant
	ABI    int
	Try    string // the self-test: a denied file the helper tries to read instead of exec'ing
	// Confine writes only through Write grants and scopes signals (ADR-0064).
	Confine bool
	// Reap keeps the helper as the command's parent, a subreaper that kills
	// whatever the command left when it ends (supervise, ADR-0064). The
	// helper it starts gets the spec without it.
	Reap bool
}

// command is an exec.Cmd that runs self as the helper with sp on fd 3. The
// helper's own environment is sp.Env, so not even the helper sees the
// node's variables.
func command(ctx context.Context, self string, sp spec) (*exec.Cmd, error) {
	f, err := specFile(sp)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, self, HelperArg)
	cmd.Env, cmd.Dir, cmd.ExtraFiles = sp.Env, "/", []*os.File{f}
	return cmd, nil
}

// Reason is why the sandbox is off; Ready when it is on.
type Reason int

// The reasons, in the order New checks them.
const (
	Ready Reason = iota
	NotLinux
	Root
	NoLandlock
	Dumpable
	SelfTestFailed
)

// reasonNames are the reasons in logs.
var reasonNames = map[Reason]string{
	Ready: "ready", NotLinux: "not_linux", Root: "root", NoLandlock: "no_landlock",
	Dumpable: "dumpable", SelfTestFailed: "self_test",
}

// String is the reason's name in logs.
func (r Reason) String() string { return reasonNames[r] }

// ErrOff is Command on a sandbox that is off: the command does not run.
var ErrOff = errors.New("sandbox: off")

// Kernel is what the sandbox needs to know of the process and its kernel.
type Kernel interface {
	Euid() int
	LandlockABI() (int, error)
	Dumpable() (bool, error)
}

// Policy is what every command is denied and the environment it gets.
type Policy struct {
	Deny     func() []string // DenyList, built again for each command; nil denies nothing
	Env      []string        // the whole environment, KEY=VALUE
	EnvRoots []string        // where .env files are looked for, besides the command's folder
	EnvDepth int
	// HideNames keeps the folders split around a denied path unlisted, so
	// the names inside denied folders stay hidden too
	// (sandbox.hide_names in ganglion.yaml, ADR-0057).
	HideNames bool
}

// denied is what the policy denies now.
func (p Policy) denied() []string {
	if p.Deny == nil {
		return nil
	}
	return slices.Clone(p.Deny())
}

// Sandbox starts commands confined by its policy, or none when it is off.
type Sandbox struct {
	self   string
	policy Policy
	abi    int
	reason Reason
	since  time.Time
}

// New checks, once, that the sandbox holds here: Linux, not root, Landlock
// present, the node not dumpable, and a self-test that runs the helper and
// fails to read a denied file. The first that fails turns it off; that
// is final until a restart (R-CMD-3, fail closed).
func New(self string, p Policy, k Kernel, now time.Time) *Sandbox {
	s := &Sandbox{self: self, policy: p, since: now}
	s.reason = s.check(k)
	return s
}

func (s *Sandbox) check(k Kernel) Reason {
	if runtime.GOOS != "linux" {
		return NotLinux
	}
	if k.Euid() == 0 {
		return Root
	}
	abi, err := k.LandlockABI()
	if err != nil {
		return NoLandlock
	}
	s.abi = abi
	if d, err := k.Dumpable(); err != nil || d {
		return Dumpable
	}
	if !s.selfTest() {
		return SelfTestFailed
	}
	return Ready
}

// selfTest runs the helper against a file it must not read.
func (s *Sandbox) selfTest() bool {
	dir, err := os.MkdirTemp("", "ganglion-sandbox-")
	if err != nil {
		return false
	}
	defer func() { _ = os.RemoveAll(dir) }()
	file := filepath.Join(dir, "secret")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		return false
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return false
	}
	grants, err := Grants([]string{resolved}, os.ReadDir, s.policy.HideNames)
	if err != nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := command(ctx, s.self, spec{Env: s.policy.Env, Dir: "/", Grants: grants, ABI: s.abi,
		Try: filepath.Join(resolved, "secret")})
	if err != nil {
		return false
	}
	defer closeSpec(cmd)
	return cmd.Run() == nil
}

// Ready reports whether commands may run, and why not.
func (s *Sandbox) Ready() (bool, Reason) { return s.reason == Ready, s.reason }

// Command is argv run in dir inside the sandbox: the caller sets stdio, a
// process group and the time limit, starts it, and calls Release after
// Start. The .env files under dir and the policy's roots are
// denied with the rest. Each KEY=VALUE of env replaces the policy's value
// of that key or is added after it; an entry without "=" is dropped.
func (s *Sandbox) Command(ctx context.Context, argv []string, dir string, env ...string) (*exec.Cmd, error) {
	sp, _, err := s.spec(argv, dir, env)
	if err != nil {
		return nil, err
	}
	return command(ctx, s.self, sp)
}

// ErrCannotConfine is Confined on a kernel whose Landlock cannot keep a
// command's writes to its folders: below ABI 3 truncating a file is not
// governed. The command does not run (fail closed, ADR-0064).
var ErrCannotConfine = errors.New("sandbox: this kernel's Landlock cannot keep a command's writes to its folders")

// minConfineABI is the first Landlock version that governs every write,
// truncation included.
const minConfineABI = 3

// writableDevices are the files outside its folders a confined command may
// write: cargo and rustc open /dev/null for the output of the programs they
// probe.
var writableDevices = []string{"/dev/null"}

// Confined is Command for code the node does not trust to write: a module's
// build, whose build script and macros come with its source (ADR-0064). It
// reads what Command reads and writes only beneath the folders of write and
// writableDevices; from Landlock ABI 6 it signals no process outside its own
// sandbox. Its helper stays as its parent and, when it ends or is sent
// SIGTERM, kills every process it left, those that left its process group
// too: the caller stops it with SIGTERM, not SIGKILL. The folders are
// resolved to their real paths; one that is denied is an error.
func (s *Sandbox) Confined(ctx context.Context, write, argv []string, dir string, env ...string) (*exec.Cmd, error) {
	sp, deny, err := s.spec(argv, dir, env)
	if err != nil {
		return nil, err
	}
	if s.abi < minConfineABI {
		return nil, ErrCannotConfine
	}
	roots := make([]string, 0, len(write))
	for _, w := range write {
		resolved, err := filepath.EvalSymlinks(w)
		if err != nil {
			return nil, fmt.Errorf("sandbox: %w", err)
		}
		roots = append(roots, resolved)
	}
	writes, err := WriteGrants(roots, deny, os.ReadDir, s.policy.HideNames)
	if err != nil {
		return nil, err
	}
	for _, d := range writableDevices {
		writes = append(writes, Grant{Path: d, Write: true})
	}
	sp.Grants, sp.Confine, sp.Reap = append(sp.Grants, writes...), true, true
	return command(ctx, s.self, sp)
}

// spec is what the helper gets for argv in dir, with the paths it denies.
func (s *Sandbox) spec(argv []string, dir string, env []string) (spec, []string, error) {
	if s.reason != Ready {
		return spec{}, nil, ErrOff
	}
	deny := s.policy.denied()
	for _, f := range EnvFiles(append([]string{dir}, s.policy.EnvRoots...), s.policy.EnvDepth, os.ReadDir) {
		if resolved, err := filepath.EvalSymlinks(f); err == nil { // Landlock sees real paths
			deny = append(deny, resolved)
		}
	}
	grants, err := Grants(deny, os.ReadDir, s.policy.HideNames)
	if err != nil {
		return spec{}, nil, err
	}
	return spec{Argv: argv, Env: mergeEnv(s.policy.Env, env), Dir: dir, Grants: grants, ABI: s.abi}, deny, nil
}

// mergeEnv is base with each KEY=VALUE of extra replacing the same key in
// place or added at the end; an entry without a key and "=" is dropped.
// base is not changed.
func mergeEnv(base, extra []string) []string {
	out := slices.Clone(base)
	for _, e := range extra {
		k, _, ok := strings.Cut(e, "=")
		if !ok || k == "" {
			continue
		}
		i := slices.IndexFunc(out, func(b string) bool { return strings.HasPrefix(b, k+"=") })
		if i < 0 {
			out = append(out, e)
			continue
		}
		out[i] = e
	}
	return out
}

// Release closes the spec file a started command no longer needs.
func Release(cmd *exec.Cmd) { closeSpec(cmd) }

func closeSpec(cmd *exec.Cmd) {
	for _, f := range cmd.ExtraFiles {
		_ = f.Close()
	}
}
