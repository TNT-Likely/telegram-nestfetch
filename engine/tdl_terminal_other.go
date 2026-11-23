//go:build !darwin && !linux && !windows

// SPDX-License-Identifier: MIT
package main

import "os/exec"

func tdlConfigureProcess(*exec.Cmd) {}
