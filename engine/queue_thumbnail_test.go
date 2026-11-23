// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestRecoverCompletedPreviewsFromSavedFiles(t *testing.T) {
	// A copy program stands in for the decoder, so each test output identifies
	// the exact source file the queue supplied without requiring a video codec.
	helper, err := exec.LookPath("cp")
	if err != nil {
		t.Skip("copy helper unavailable")
	}
	data, folder := t.TempDir(), t.TempDir()
	cache := filepath.Join(data, "thumbnails")
	if err := os.Mkdir(cache, 0700); err != nil {
		t.Fatal(err)
	}
	state := queueState{Version: 1, Folder: folder}
	expected := make(map[string]string)
	for i, name := range []string{"old", "cache-lost", "already-cached", "spoiler", "changed", "missing", "offline"} {
		id := fmt.Sprintf("%032x", i+1)
		entry := &queueEntry{Job: Job{ID: id, URL: mediaURL, State: completed, Folder: folder, Name: name + ".webm"}}
		if name == "offline" {
			entry.Folder = filepath.Join(folder, "unmounted")
		}
		entry.OutputPath = filepath.Join(entry.Folder, entry.Name)
		source := append([]byte{0xff, 0xd8, 0xff}, name...)
		entry.Size = int64(len(source))
		if name != "missing" && name != "offline" {
			if err := os.WriteFile(entry.OutputPath, source, 0400); err != nil {
				t.Fatal(err)
			}
		}
		switch name {
		case "cache-lost", "old":
			entry.HasThumbnail = name == "cache-lost"
			expected[id] = "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(source)
		case "already-cached":
			entry.HasThumbnail = true
			expected[id] = jpeg
			if err := os.WriteFile(filepath.Join(cache, id+".txt"), []byte(jpeg), 0600); err != nil {
				t.Fatal(err)
			}
		case "spoiler":
			entry.HideThumbnail = true
		case "changed":
			entry.Size++
		}
		state.Jobs = append(state.Jobs, entry)
	}
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "queue.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(folder, 0500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(folder, 0700) })
	provider := fakeTelegram("disconnected")
	q, err := openQueue(context.Background(), data, folder, helper, provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.close)
	// Newest jobs are repaired first. Once the oldest is ready all available
	// outputs have been inspected, and the absent volume is waiting to retry.
	oldest := state.Jobs[0].ID
	deadline := time.Now().Add(5 * time.Second)
	for {
		image, err := q.thumbnail(oldest)
		if err != nil {
			t.Fatal(err)
		}
		if image == expected[oldest] {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the completed WebM did not receive its own preview")
		}
		time.Sleep(10 * time.Millisecond)
	}
	q.close()

	q, err = openQueue(context.Background(), data, folder, "", provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.close)
	for _, job := range q.snapshot().Jobs {
		want := expected[job.ID]
		image, err := q.thumbnail(job.ID)
		if err != nil || job.State != completed || job.HasThumbnail != (want != "") ||
			(want != "" && image != want) || (want == "" && image != nil) {
			t.Errorf("%s: saved preview = %v, state = %s, error = %v", job.Name, image, job.State, err)
		}
	}
	if offsets := provider.copyOffsets(); len(offsets) != 0 {
		t.Fatalf("restoring previews downloaded media again: %v", offsets)
	}
	if entries, err := os.ReadDir(folder); err != nil || len(entries) != 5 {
		t.Fatalf("the download directory changed when generating previews: %v, %v", entries, err)
	}
}
