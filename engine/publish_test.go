// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishRecoversInterruptedCommit(t *testing.T) {
	for _, existing := range []string{videoA, videoB} {
		t.Run(existing, func(t *testing.T) {
			if len(existing) != len(videoA) {
				t.Fatal("test videos must be the same size")
			}
			data, destination := t.TempDir(), t.TempDir()
			f := fakeTelegram("disconnected")
			q, err := openQueue(context.Background(), data, destination, "", f)
			if err != nil {
				t.Fatal(err)
			}
			defer q.close()
			id, err := q.enqueue(mediaURL)
			if err != nil {
				t.Fatal(err)
			}
			part := q.partPath(id)
			if err := os.Mkdir(filepath.Dir(part), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(part, []byte(videoA), 0600); err != nil {
				t.Fatal(err)
			}
			name := strings.Repeat("a", 191) + ".mp4"
			outputName := candidateName(name, 2)
			output := filepath.Join(destination, outputName)
			if err := os.WriteFile(output, []byte(existing), 0600); err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256([]byte(videoA))
			intent := &saveIntent{
				Temporary: tempPrefix + id + ".part",
				Name:      outputName,
				Digest:    hex.EncodeToString(digest[:]),
			}
			if !intent.valid() {
				t.Fatalf("save journal rejects its own collision filename: %+v", intent)
			}

			q.mu.Lock()
			job := q.byIDLocked(id)
			job.State, job.Name, job.DocumentID, job.Size = saving, name, 113, int64(len(videoA))
			job.Save, job.TransferDone = intent, true
			err = q.persistLocked()
			q.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			q.close()
			q, err = openQueue(context.Background(), data, destination, "", f)
			if err != nil {
				t.Fatal(err)
			}
			defer q.close()

			finished := expectState(t, q, id, completed)
			wanted := filepath.Join(destination, name)
			if existing == videoA {
				wanted = output
			}
			if finished.OutputPath != wanted || readFile(t, finished.OutputPath) != videoA {
				t.Fatalf("incorrect recovery output: %+v", finished)
			}
			if readFile(t, output) != existing {
				t.Fatal("recovery overwrote an existing file")
			}
			if n := len(f.copyOffsets()); n != 0 {
				t.Fatalf("publish must work offline, got %d transfers", n)
			}
		})
	}
}

func TestPublishDoesNotReplaceExisting(t *testing.T) {
	folder, err := openFolder(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer folder.close()
	for name, contents := range map[string]string{"source": "incoming", "destination": "existing"} {
		if err := folder.root.WriteFile(name, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
	}
	err = commitOutput(folder, "source", "destination")
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("collision was not reported: %v", err)
	}
	if got := readFile(t, filepath.Join(folder.path, "destination")); got != "existing" {
		t.Fatalf("original output changed to %q", got)
	}
}

func TestSaveFilenameIsPortable(t *testing.T) {
	for raw, want := range map[string]string{
		"../../../video.mov": "_.._.._video.mov",
		"file:backup?.mp4 ":  "file_backup_.mp4",
		"CON.mp4":            "_CON.mp4",
		"LPT¹.mkv":           "_LPT¹.mkv",
		"...":                "video.mp4",
	} {
		if got := filename(raw); got != want || !validOutputName(got) {
			t.Errorf("filename(%q) = %q, want %q", raw, got, want)
		}
	}
	got := filename(strings.Repeat("视频", 100) + ".mp4")
	if !validOutputName(got) || !strings.HasSuffix(got, ".mp4") {
		t.Fatalf("UTF-8 filename truncated incorrectly: %q", got)
	}
}

func TestExclusiveEngineLock(t *testing.T) {
	dir := t.TempDir()
	first, err := lockDataDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := lockDataDirectory(dir); err == nil {
		second.Close()
		first.Close()
		t.Fatal("a second engine locked the same database")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	last, err := lockDataDirectory(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = last.Close() })
}
