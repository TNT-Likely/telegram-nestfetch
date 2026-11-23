//go:build darwin || linux

// SPDX-License-Identifier: MIT
package main

import (
	"os/exec"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func tdlConfigureProcess(*exec.Cmd) {}

func startTDLTerminal(cmd *exec.Cmd) (tdlTerminal, error) {
	master, slave, err := pty.Open()
	if err != nil {
		return tdlTerminal{}, err
	}
	defer slave.Close()
	if err = pty.Setsize(master, &pty.Winsize{Rows: tdlTerminalRows, Cols: tdlTerminalCols}); err == nil {
		// Set no echo before the child displays its password prompt. The
		// survey implementation in tdl subsequently configures its own mode.
		_, err = term.MakeRaw(int(slave.Fd()))
	}
	if err != nil {
		master.Close()
		return tdlTerminal{}, err
	}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true}
	if err := cmd.Start(); err != nil {
		master.Close()
		return tdlTerminal{}, err
	}
	return tdlTerminal{output: master, write: master.Write, passwordTerminator: '\r'}, nil
}
