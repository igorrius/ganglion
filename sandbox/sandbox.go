// Package sandbox runs host commands and skill scripts so that they cannot
// gain privileges, see the node's variables, or read or write the node's data
// and secret files and the owner's secret stores (R-CMD-3, ADR-0039).
package sandbox

import (
	"context"
	"os"
	"os/exec"
)

// HelperArg is the argument that makes the ganglion binary the sandbox's
// helper: `ganglion __sandbox`, with the spec on file descriptor 3.
const HelperArg = "__sandbox"

// exitSetup is the helper's exit code when it could not set up the
// sandbox or start the command: the command never ran.
const exitSetup = 125

// exitReadable is the helper's exit code when the self-test could read
// the file it should not (spec.Try).
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
