//go:build !linux && !darwin

package claudecli

import "os/exec"

// ownGroup keeps exec's default on platforms without process groups:
// a cancelled context kills claude only.
func ownGroup(*exec.Cmd) {}
