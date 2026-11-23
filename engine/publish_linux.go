// SPDX-License-Identifier: MIT
package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func folderAvailable(path string, dir *os.File) bool {
	return sameOpenedFolder(path, dir)
}

func commitOutput(folder *folderHandle, source, destination string) error {
	fd := int(folder.dir.Fd())
	err := unix.Renameat2(fd, source, fd, destination, unix.RENAME_NOREPLACE)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		return folder.root.Link(source, destination)
	}
	return err
}
