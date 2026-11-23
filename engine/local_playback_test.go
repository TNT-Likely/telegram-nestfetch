// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/TNT-Likely/telegram-nestfetch/engine/preview"
)

func TestSavedVideoPlaybackChecksJobAndFile(t *testing.T) {
	folder := t.TempDir()
	saved := filepath.Join(folder, "video.mp4")
	if err := os.WriteFile(saved, []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), filepath.Base(saved))
	if err := os.WriteFile(outside, []byte("sample"), 0600); err != nil {
		t.Fatal(err)
	}
	valid := queueEntry{Job: Job{State: completed, Kind: MediaVideo, Size: 6, Folder: folder, OutputPath: saved}}
	check := func(t *testing.T, entry queueEntry, code string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		err := playSavedVideo(ctx, entry, func(string) { t.Error("unexpected public URL") })
		expectCode(t, err, code)
	}
	for _, tc := range []struct {
		name   string
		code   string
		change func(*queueEntry)
	}{
		{"incomplete", "invalid_state", func(e *queueEntry) { e.State = downloading }},
		{"document", "preview_video_required", func(e *queueEntry) { e.Kind = MediaFile }},
		{"wrong folder", "file_missing", func(e *queueEntry) { e.OutputPath = outside }},
		{"moved file", "file_missing", func(e *queueEntry) { e.OutputPath = filepath.Join(folder, "missing.mp4") }},
		{"changed size", "file_missing", func(e *queueEntry) { e.Size++ }},
		{"unmounted folder", "folder_unavailable", func(e *queueEntry) {
			e.Folder = filepath.Join(folder, "unmounted")
			e.OutputPath = filepath.Join(e.Folder, "video.mp4")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := valid
			tc.change(&entry)
			check(t, entry, tc.code)
		})
	}
	t.Run("symbolic link", func(t *testing.T) {
		entry := valid
		entry.OutputPath = filepath.Join(folder, "linked.mp4")
		if err := os.Symlink(outside, entry.OutputPath); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		check(t, entry, "file_missing")
	})
}

func TestSavedVideoPlaybackSeeksNearEndOfLargeFile(t *testing.T) {
	folder := t.TempDir()
	source := filepath.Join(folder, "video.webm")
	file, err := os.OpenFile(source, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	base := int64(40 * 1024 * 1024)
	if runtime.GOOS == "darwin" || runtime.GOOS == "linux" {
		// A sparse file tests offsets beyond 32 bits without storing gigabytes.
		base = 1 << 32
	}
	start := base + preview.BlockSize - 3
	stamp := []byte("local video data across a cache block")
	size := start + int64(len(stamp))
	if _, err := file.WriteAt(stamp, start); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	entry := queueEntry{Job: Job{Folder: folder, OutputPath: source, State: completed, Kind: MediaVideo, Size: size}}
	urls, done := make(chan string, 1), make(chan error, 1)
	go func() {
		done <- playSavedVideo(ctx, entry, func(address string) { urls <- address })
	}()
	var address string
	select {
	case address = <-urls:
	case err := <-done:
		t.Fatalf("video playback failed to start: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-", start))
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(len(stamp)+1)))
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusPartialContent || !bytes.Equal(data, stamp) ||
		resp.Header.Get("Content-Type") != "video/webm" ||
		resp.Header.Get("Content-Range") != fmt.Sprintf("bytes %d-%d/%d", start, size-1, size) {
		t.Fatalf("unexpected local range response: %v %v %q %v", resp.Status, resp.Header, data, err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("playback shutdown: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("playback did not close")
	}
}
