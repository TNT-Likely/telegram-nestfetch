//go:build !darwin && !linux && !windows

// SPDX-License-Identifier: MIT
package main

import "errors"

type unsupportedLock struct{}

func (unsupportedLock) Close() error { return nil }

func lockDataDirectory(string) (unsupportedLock, error) {
	return unsupportedLock{}, errors.New("download engine isn't available for this platform")
}
