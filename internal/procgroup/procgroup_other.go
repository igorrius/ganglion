//go:build !unix

// Package procgroup runs a child in a process group of its own, so a time
// limit or the end of its work kills everything it started (R-CMD-4,
// ADR-0042). Shared by skill scripts, host commands and the claude CLI.
package procgroup

import "os/exec"

// Own keeps exec's default where there are no process groups: a cancelled
// context kills the child only.
func Own(*exec.Cmd) {}

// Kill does nothing where there are no process groups.
func Kill(*exec.Cmd) {}
