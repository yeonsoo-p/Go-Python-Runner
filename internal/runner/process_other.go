//go:build !windows

package runner

import "os/exec"

// hideConsole is a no-op on non-Windows platforms.
func hideConsole(_ *exec.Cmd) {}
