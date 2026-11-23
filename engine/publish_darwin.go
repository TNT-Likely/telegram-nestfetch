// SPDX-License-Identifier: MIT
package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func folderAvailable(path string, dir *os.File) bool {
	if !sameOpenedFolder(path, dir) {
		return false
	}
	parts := strings.Split(filepath.Clean(path), string(filepath.Separator))
	if len(parts) < 2 || !strings.EqualFold(parts[1], "Volumes") {
		return true
	}
	if len(parts) < 3 || parts[2] == "" {
		return false
	}
	var volumes, mounted, opened unix.Stat_t
	mount := filepath.Join("/Volumes", parts[2])
	return unix.Stat("/Volumes", &volumes) == nil && unix.Stat(mount, &mounted) == nil &&
		unix.Fstat(int(dir.Fd()), &opened) == nil && volumes.Dev != mounted.Dev && mounted.Dev == opened.Dev
}

func commitOutput(folder *folderHandle, source, destination string) error {
	fd := int(folder.dir.Fd())
	err := unix.RenameatxNp(fd, source, fd, destination, unix.RENAME_EXCL)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) {
		return folder.root.Link(source, destination)
	}
	return err
}
