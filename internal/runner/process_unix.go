//go:build !windows

package runner

import "os/exec"

// hideConsole is a no-op on non-Windows platforms.
func hideConsole(_ *exec.Cmd) {}

// HideConsole is the exported no-op variant for non-Windows callers; see
// the Windows file for the rationale.
func HideConsole(_ *exec.Cmd) {}
