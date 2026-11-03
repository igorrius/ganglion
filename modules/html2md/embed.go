// Package html2md holds the built html2md module and its manifest
// (ADR-0035). The module is a Rust crate in this directory; `make
// modules` rebuilds html2md.wasm, which is committed so that a Go build
// needs no Rust toolchain. Updating the module's HTML parser is shipping
// a new html2md.wasm, without a core release.
package html2md

import _ "embed" // the module and its manifest

// Wasm is the built module.
//
//go:embed html2md.wasm
var Wasm []byte

// Manifest is the module's manifest; it grants nothing.
//
//go:embed html2md.yaml
var Manifest []byte
