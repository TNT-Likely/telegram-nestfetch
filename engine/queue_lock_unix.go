//go:build darwin || linux

// SPDX-License-Identifier: MIT
package main

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

func lockDataDirectory(path string) (*os.File, error) {
	file, err := os.OpenFile(filepath.Join(path, "engine.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("download engine is already running")
		}
		return nil, err
	}
	return file, nil
}
