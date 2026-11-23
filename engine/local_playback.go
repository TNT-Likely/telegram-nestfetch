// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"os"
	"path/filepath"

	"github.com/TNT-Likely/telegram-nestfetch/engine/preview"
)

func playSavedVideo(ctx context.Context, entry queueEntry, ready func(string)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry.State != completed || entry.OutputPath == "" {
		return &Failure{Code: "invalid_state"}
	}
	if entry.Kind != MediaVideo {
		return &Failure{Code: "preview_video_required"}
	}

	name := filepath.Base(entry.OutputPath)
	if entry.OutputPath != filepath.Join(entry.Folder, name) || !validOutputName(name) || entry.Size <= 0 {
		return &Failure{Code: "file_missing"}
	}
	folder, err := openFolder(entry.Folder)
	if err != nil {
		return err
	}
	defer folder.close()

	info, err := folder.root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != entry.Size {
		return &Failure{Code: "file_missing"}
	}
	file, err := folder.root.Open(name)
	if err != nil {
		return &Failure{Code: "file_missing"}
	}
	defer file.Close()

	// Confirm that the opened file is still the original regular directory
	// entry before publishing a URL, even if the path changed while opening.
	opened, err := file.Stat()
	after, statErr := folder.root.Lstat(name)
	if err != nil || statErr != nil || !after.Mode().IsRegular() || !opened.Mode().IsRegular() ||
		opened.Size() != entry.Size || !os.SameFile(info, opened) || !os.SameFile(after, opened) {
		return &Failure{Code: "file_missing"}
	}
	if !folderAvailable(folder.path, folder.dir) {
		return &Failure{Code: "folder_unavailable"}
	}

	read := func(ctx context.Context, offset int64, block []byte) (int, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		return file.ReadAt(block, offset)
	}
	err = preview.Serve(ctx, entry.Size, preview.ContentType("", name), read, func(address string) error {
		ready(address)
		return nil
	}, nil)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return &Failure{Code: "preview_unavailable"}
	}
	return nil
}
