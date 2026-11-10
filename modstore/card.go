package modstore

import "errors"

// RunState is how a local module stands in the node, as /modules shows it.
type RunState string

// Run states.
const (
	RunRunning   RunState = "running"
	RunOff       RunState = "off"       // the owner turned it off
	RunCrashloop RunState = "crashloop" // the node turned it off after five failures within an hour
	RunStopped   RunState = "stopped"   // it should run and does not: see /status
)

// LocalModule is one local module in the list of /modules.
type LocalModule struct {
	Name    string // the short name, "deye-reader"
	State   RunState
	Version string // the active version's number
}

// ConfigLine is one config key the manifest declares, with what the module
// reads for it now. The words are the manifest's: source text, shown to the
// owner only.
type ConfigLine struct {
	Key      string
	Type     string // string, number or boolean
	Required bool
	Default  string
	Value    string // the settings' value; "" when none is set
	Set      bool
}

// Card is what /modules <name> shows: how a module stands and every version
// it has had.
type Card struct {
	LocalModule
	Journal []Entry // newest first, the retired ones too
	Config  []ConfigLine
	Health  Health // the running instance's health check; "" when it is not running
}

// Health is how a module's health check went (R-RUN-12): HealthOK, or the
// kind it failed with, in the node's words (module.exception, auth_failed,
// stale, module.format); "" when it did not run.
type Health string

// HealthOK is a health check that passed, or a module that has none.
const HealthOK Health = "ok"

// Config refusals.
var (
	// ErrUndeclaredKey is a config key the manifest does not declare.
	ErrUndeclaredKey = errors.New("modstore: the manifest declares no such config key")
	// ErrWrongType is a config value that is not of its key's type.
	ErrWrongType = errors.New("modstore: the config value is not of its key's type")
	// ErrNotStarted is a module that is active and on but could not start;
	// the error names why in the node's words.
	ErrNotStarted = errors.New("modstore: the module did not start")
)

// ConfigError is a refused config value. It wraps ErrUndeclaredKey or
// ErrWrongType; it never holds the value.
type ConfigError struct {
	Err      error
	Key      string
	Type     string   // the key's declared type, for ErrWrongType
	Declared []string // the declared keys, sorted, for ErrUndeclaredKey
}

func (e *ConfigError) Error() string { return e.Err.Error() + ": " + e.Key }

// Unwrap returns the sentinel.
func (e *ConfigError) Unwrap() error { return e.Err }
