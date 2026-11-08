package config

import (
	"os"
	"testing"

	"github.com/igorrius/ganglion/sandbox"
)

// TestMain lets the test binary be the sandbox's helper, as the ganglion
// binary is: the node's sandbox runs commands through its own image.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.HelperArg {
		os.Exit(sandbox.Helper())
	}
	os.Exit(m.Run())
}
