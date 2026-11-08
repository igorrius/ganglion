//go:build lab

package chatlab

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/igorrius/ganglion/sandbox"
)

// TestMain makes the test binary what chatlab's binary is: the sandbox's
// helper, and not dumpable, so a lab node's command sandbox can be ready.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperArg {
		os.Exit(sandbox.Helper())
	}
	if err := sandbox.Harden(); err != nil {
		fmt.Fprintln(os.Stderr, "chatlab: the process stays dumpable, so the sandbox is off:", err)
	}
	os.Exit(m.Run())
}

// sandboxReady reports whether a lab node's command sandbox can be ready on
// this machine: Linux with Landlock, not root, the process not dumpable.
func sandboxReady() bool {
	ok, _ := sandbox.New("/proc/self/exe", sandbox.Policy{}, sandbox.HostKernel(), time.Now()).Ready()
	return ok
}

// runsCommands reports whether a scenario's script runs host commands, which
// need the sandbox.
func runsCommands(s Scenario) bool {
	for _, st := range s.Script {
		if st.Tool == "host.exec" {
			return true
		}
	}
	return false
}
