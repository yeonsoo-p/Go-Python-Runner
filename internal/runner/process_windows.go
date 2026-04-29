package runner

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// HideConsole prevents the child process from opening a visible console window.
// The Wails app is built with -H windowsgui, so any subprocess we spawn would
// otherwise flash a black console window — this suppresses that.
func HideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow,
	}
}
