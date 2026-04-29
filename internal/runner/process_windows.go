package runner

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// hideConsole prevents the child process from opening a visible console window.
// Internal callers in this package use this directly.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow,
	}
}

// HideConsole is the exported variant for callers outside the runner package
// (e.g. EnvService shelling out to pip / uv). The Wails app is built with
// -H windowsgui, so any subprocess we spawn would otherwise flash a black
// console window — this suppresses that.
func HideConsole(cmd *exec.Cmd) { hideConsole(cmd) }
