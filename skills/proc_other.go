//go:build !linux && !darwin

package skills

import "os/exec"

// ownGroup keeps exec's default where there are no process groups: a
// cancelled context kills the script only.
func ownGroup(*exec.Cmd) {}

func killGroup(*exec.Cmd) {}
