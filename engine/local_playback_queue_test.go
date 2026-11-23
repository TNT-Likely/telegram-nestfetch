// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCompletedVideoPlaysOfflineWithoutBlockingQueue(t *testing.T) {
	data, folder := t.TempDir(), t.TempDir()
	path := filepath.Join(folder, "previous.mp4")
	sample := []byte("saved video bytes")
	if err := os.WriteFile(path, sample, 0600); err != nil {
		t.Fatal(err)
	}
	id := "00000000000000000000000000000001"
	state := queueState{Version: 1, Folder: folder, Jobs: []*queueEntry{{Job: Job{
		ID: id, URL: mediaURL, State: completed, Name: "previous.mp4", Size: int64(len(sample)),
		Folder: folder, OutputPath: path, // Legacy records didn't have a media kind.
	}}}}
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "queue.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}

	p := fakeTelegram("disconnected")
	q, err := openQueue(context.Background(), data, folder, "", p)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()

	playback, err := q.startLocalPlayback(id)
	if err != nil {
		t.Fatal(err)
	}
	var address string
	until := time.Now().Add(4 * time.Second)
	for {
		state, err := q.mediaTaskStatus("preview", playback)
		if err != nil || state.State == "failed" || time.Now().After(until) {
			t.Fatalf("local playback did not start offline: %+v %v", state, err)
		}
		if state.State == "ready" && state.URL != "" {
			address = state.URL
			if state.Waiting || state.Speed != 0 || state.BytesRead != 0 {
				t.Fatalf("local playback reported Telegram work: %+v", state)
			}
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	p.setAccount("connected")
	other, err := q.enqueue("https://t.me/sample_channel/212")
	if err != nil {
		t.Fatal(err)
	}
	expectState(t, q, other, completed)
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Head(address)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.ContentLength != int64(len(sample)) {
		t.Fatalf("playback interrupted by download: status=%d bytes=%d", resp.StatusCode, resp.ContentLength)
	}

	q.cancelTelegramTask(playback, true)
	_, err = q.mediaTaskStatus("preview", playback)
	expectCode(t, err, "not_found")
}
