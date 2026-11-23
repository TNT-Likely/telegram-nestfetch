//go:build !darwin && !linux

// SPDX-License-Identifier: MIT
package main

import (
	"os"
	"os/exec"
)

func startTDLTerminal(cmd *exec.Cmd) (tdlTerminal, error) {
	reader, writer, err := os.Pipe()
	if err != nil {
		return tdlTerminal{}, err
	}
	defer writer.Close()
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		reader.Close()
		return tdlTerminal{}, err
	}
	return tdlTerminal{output: reader}, nil
}
