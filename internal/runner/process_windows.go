package runner

import (
	"os/exec"
	"syscall"
)

const createNoWindow = 0x08000000

// hideConsole prevents the child process from opening a visible console window.
func hideConsole(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNoWindow,
	}
}
