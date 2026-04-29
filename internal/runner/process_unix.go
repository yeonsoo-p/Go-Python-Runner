//go:build !windows

package runner

import "os/exec"

// HideConsole is a no-op on non-Windows platforms; see the Windows file for
// the rationale.
func HideConsole(_ *exec.Cmd) {}
