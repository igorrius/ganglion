// Package flags declares the command line's flags: each with its
// environment variable, default and help category, in one place
// (ADR-0029). Settings live in ganglion.yaml (ADR-0059); only where the file
// and the database are, and which roles run, stay flags. Secrets are never
// flags: they come from the secrets package.
package flags

import (
	"os"
	"strconv"
	_ "time/tzdata" // node.tz works in images without zoneinfo

	"github.com/urfave/cli/v3"
)

// Help categories.
const (
	catStorage   = "storage"
	catTelemetry = "telemetry"
)

// Run holds the flags of `ganglion run`.
type Run struct {
	// Config is the path of ganglion.yaml; empty is ganglion.yaml beside
	// the database.
	Config string
	DB     string
	Roles  string
	// ImageEnv is the file that holds the image's environment level; empty
	// or missing means the node knows no image.
	ImageEnv string
}

// Flags returns the flags of `ganglion run` bound to r.
func (r *Run) Flags() []cli.Flag {
	return []cli.Flag{
		ConfigFlag(&r.Config),
		DBFlag(&r.DB),
		ImageEnvFlag(&r.ImageEnv),
		&cli.StringFlag{Name: "roles", Usage: "comma-separated roles to run: core, edge, worker", Value: "core,edge,worker",
			Local: true, Destination: &r.Roles},
	}
}

// setEnv is an environment variable that counts only when it is not
// empty, as an unset one did before the flags.
type setEnv string

func env(key string) cli.ValueSourceChain { return cli.NewValueSourceChain(setEnv(key)) }

func (e setEnv) Lookup() (string, bool) {
	v, ok := os.LookupEnv(string(e))
	return v, ok && v != ""
}
func (e setEnv) IsFromEnv() bool  { return true }
func (e setEnv) Key() string      { return string(e) }
func (e setEnv) String() string   { return "environment variable " + strconv.Quote(string(e)) }
func (e setEnv) GoString() string { return "setEnv(" + strconv.Quote(string(e)) + ")" }

// ConfigFlag is --config, shared by `run` and `mcp`: the node's settings
// file (ADR-0059).
func ConfigFlag(dst *string) cli.Flag {
	return &cli.StringFlag{Name: "config", Usage: "the settings file, which must exist; default: ganglion.yaml beside the database, defaults when missing", Category: catStorage,
		Local: true, Sources: env("GANGLION_CONFIG"), Destination: dst, TakesFile: true}
}

// DBFlag is --db, shared by `run` and `mcp`.
func DBFlag(dst *string) cli.Flag {
	return &cli.StringFlag{Name: "db", Usage: "the SQLite database file", Value: "ganglion.db", Category: catStorage,
		Local: true, Sources: env("GANGLION_DB"), Destination: dst, TakesFile: true}
}

// DefaultImageEnv is where the image keeps its environment level.
const DefaultImageEnv = "/usr/local/share/ganglion/image-env"

// ImageEnvFlag is --image-env, shared by `run` and `update`: the file that
// holds the image's environment level, which a release is checked against
// (ADR-0081). A node outside the image has no such file.
func ImageEnvFlag(dst *string) cli.Flag {
	return &cli.StringFlag{Name: "image-env", Usage: "the file with the image's environment level; missing means no image", Value: DefaultImageEnv,
		Category: catStorage, Local: true, Sources: env("GANGLION_IMAGE_ENV"), Destination: dst, TakesFile: true}
}

// LogDirFlag is --log-dir of `ganglion mcp`: where the log files are, when
// the diagnostics are wanted without the node's settings file (ADR-0043).
// Empty takes log.dir from the file.
func LogDirFlag(dst *string) cli.Flag {
	return &cli.StringFlag{Name: "log-dir", Usage: "the log files' directory, or off; default: log.dir of the settings file",
		Category: catTelemetry, Local: true, Destination: dst, TakesFile: true}
}
