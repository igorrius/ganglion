// Package builder turns a module's source in the workspace into the two files
// the module store installs, a WebAssembly binary and its manifest (R-RUN-9,
// ADR-0061). The build runs in the command sandbox on a temporary copy of the
// source and leaves nothing behind.
package builder

import (
	"context"
	"errors"
)

// Language is what a module is written in.
type Language string

// The languages a source folder may be in. Only Rust is built in M4
// (ADR-0063): the others are detected and refused with ErrNoBuilder.
const (
	Rust   Language = "rust"
	Go     Language = "go"
	TinyGo Language = "tinygo"
)

// Abilities are the languages this node can build, each with its toolchain's
// version line.
type Abilities map[Language]string

// Source is a module's source folder in the workspace.
type Source struct {
	Name string // short name, e.g. "deye-reader"
	Dir  string // the folder in the workspace
}

// Artifact is what a build made.
type Artifact struct {
	Wasm     []byte
	Manifest []byte // the source folder's manifest.yaml
	Language Language
	// SHA256 is the build's identity, the digest modstore.Digest makes over
	// both files: the sha256 of the sha256 of the wasm followed by the sha256
	// of the manifest, in hex.
	SHA256 string
}

// Log is the end of the build's output, stdout and stderr together, for the
// agent. It is the build tool's text and the source's: outside content.
type Log string

// Builder builds modules.
type Builder interface {
	// Can says which languages this node builds, with their toolchains.
	Can(ctx context.Context) (Abilities, error)
	// Build builds src. The log comes back with a failed build too.
	Build(ctx context.Context, src Source) (Artifact, Log, error)
}

// ErrNoBuilder is a source in a language this node has no toolchain or no
// builder for.
var ErrNoBuilder = errors.New("builder: no builder for this language on this node")
