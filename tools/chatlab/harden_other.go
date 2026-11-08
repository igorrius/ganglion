//go:build lab && !linux

package main

// harden has nothing to do where the sandbox is off anyway.
func harden() {}
