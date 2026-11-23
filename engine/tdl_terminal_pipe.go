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
	input, err := cmd.StdinPipe()
	if err != nil {
		reader.Close()
		return tdlTerminal{}, err
	}
	// Select the bundled tdl's line-based password prompt without a console.
	cmd.Env = append(cmd.Environ(), "TDL_NESTFETCH_PASSWORD_STDIN=1")
	cmd.Stdout, cmd.Stderr = writer, writer
	if err := cmd.Start(); err != nil {
		input.Close()
		reader.Close()
		return tdlTerminal{}, err
	}
	return tdlTerminal{output: reader, write: input.Write, passwordTerminator: '\n'}, nil
}
