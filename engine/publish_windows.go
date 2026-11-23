// SPDX-License-Identifier: MIT
package main

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func folderAvailable(path string, dir *os.File) bool {
	return sameOpenedFolder(path, dir)
}

func commitOutput(folder *folderHandle, source, destination string) error {
	// MoveFile is an exclusive rename for two paths on the same volume.
	from, err := windows.UTF16PtrFromString(filepath.Join(folder.path, source))
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(filepath.Join(folder.path, destination))
	if err != nil {
		return err
	}
	return windows.MoveFile(from, to)
}
