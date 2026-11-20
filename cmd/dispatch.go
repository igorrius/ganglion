package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/igorrius/ganglion/update"
)

// dispatched are the host commands that run as the node's active release
// (R-UPD-9, ADR-0096). `run` has its launcher; `mcp-relay` and `sandbox`
// are started by the running node itself.
var dispatched = map[string]bool{"version": true, "mcp": true, "secret": true, "update": true, "backup": true}

// Dispatch hands a host command to the node's active release when this
// binary is not it; main calls it before Execute. It returns when this
// process is the one to run the command, and never stops it.
func Dispatch(args []string, errOut io.Writer) {
	if len(args) < 2 || !dispatched[args[1]] {
		return
	}
	dir, ok := releasesDir(dbFromArgs(args[2:], os.Getenv))
	if !ok {
		return
	}
	store, err := update.Open(dir)
	if err != nil {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	update.Dispatcher{Store: store, Self: self, Image: imageVersion(), UID: os.Geteuid(), Owner: fileOwner, Exec: execBinary, Log: errOut}.Dispatch()
}

// dbFromArgs is the database a command line names, the way its --db flag
// reads it: the flag, else GANGLION_DB, else ganglion.db.
func dbFromArgs(args []string, getenv func(string) string) string {
	for i, a := range args {
		if a == "--" {
			break
		}
		name, value, eq := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if !strings.HasPrefix(a, "-") || name != "db" {
			continue
		}
		if eq {
			return value
		}
		if i+1 < len(args) {
			return args[i+1]
		}
	}
	if db := getenv("GANGLION_DB"); db != "" {
		return db
	}
	return "ganglion.db"
}
