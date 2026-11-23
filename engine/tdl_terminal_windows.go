// SPDX-License-Identifier: MIT
package main

import (
	"os/exec"
	"syscall"
)

func tdlConfigureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: 0x08000000} // CREATE_NO_WINDOW
}
