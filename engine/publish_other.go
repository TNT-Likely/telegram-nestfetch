//go:build !darwin && !linux && !windows

// SPDX-License-Identifier: MIT
package main

import "os"

func folderAvailable(path string, dir *os.File) bool {
	return sameOpenedFolder(path, dir)
}

func commitOutput(folder *folderHandle, source, destination string) error {
	return folder.root.Link(source, destination)
}
