//go:build linux

package sandbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"golang.org/x/sys/unix"
)

// Helper is the sandbox's helper: it reads the spec from fd 3, sets
// no_new_privs, confines itself with Landlock and execs the command, on
// one locked thread, so the flag and the domain are the ones exec carries.
// It returns only when that fails, with exitSetup: the command never ran.
// For a spec with Reap it supervises instead, and returns the command's
// exit code (supervise).
func Helper() int {
	runtime.LockOSThread()
	sp, err := readSpec()
	if err == nil && sp.Reap {
		return supervise(sp)
	}
	if err == nil {
		err = confine(sp)
	}
	fmt.Fprintln(os.Stderr, "ganglion sandbox:", err)
	return exitSetup
}

// readSpec reads the spec the node wrote on fd 3.
func readSpec() (spec, error) {
	f := os.NewFile(specFD, "sandbox-spec")
	var sp spec
	err := json.NewDecoder(f).Decode(&sp)
	_ = f.Close()
	if err != nil {
		return spec{}, fmt.Errorf("spec: %w", err)
	}
	if len(sp.Argv) == 0 && sp.Try == "" {
		return spec{}, errors.New("spec: no command")
	}
	return sp, nil
}

// confine sets no_new_privs, Landlock and seccomp on this thread and execs
// the command, or runs the self-test; it returns only on a failure.
func confine(sp spec) error {
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("no_new_privs: %w", err)
	}
	if err := restrict(sp.ABI, sp.Grants, sp.Confine); err != nil {
		return err
	}
	if err := refuseUnixSockets(sp.Confine); err != nil {
		return err
	}
	if sp.Try != "" {
		try(sp.Try)
	}
	if err := os.Chdir(sp.Dir); err != nil { // first, so ./script is found in its folder
		return err
	}
	prog, err := exec.LookPath(sp.Argv[0]) // PATH is the spec's: the helper's environment is sp.Env
	if err != nil {
		return err
	}
	return unix.Exec(prog, sp.Argv, sp.Env)
}

// try is the self-test: it exits 0 when path is denied and a Unix socket
// cannot be opened, and exitReadable when either works.
func try(path string) {
	if _, err := os.ReadFile(path); !errors.Is(err, os.ErrPermission) {
		os.Exit(exitReadable)
	}
	if fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM, 0); err == nil || !errors.Is(err, unix.EPERM) {
		if err == nil {
			_ = unix.Close(fd)
		}
		os.Exit(exitReadable)
	}
	os.Exit(0)
}
