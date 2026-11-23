// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

const (
	mediaURL = "https://t.me/sample_channel/42"
	videoA   = "video bytes from Telegram"
	videoB   = "different bytes with same"
	jpeg     = "data:image/jpeg;base64,/9j/"
)

type testTelegram struct {
	mu          sync.Mutex
	state       string
	media       Media
	offsets     []int64
	pauseFirst  bool
	reachedPart chan struct{}
}

func fakeTelegram(state string) *testTelegram {
	return &testTelegram{
		state: state,
		media: Media{ID: 113, Kind: MediaVideo, Name: "clip.mp4", Title: "Clip", Source: "@sample", Bytes: int64(len(videoA)), Duration: 6.5, Thumbnail: jpeg},
	}
}

func (f *testTelegram) Status() Account {
	f.mu.Lock()
	defer f.mu.Unlock()
	return Account{State: f.state}
}

func (f *testTelegram) setAccount(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.state = state
}

func (f *testTelegram) Configure(APIConfig) error { return nil }
func (f *testTelegram) StartLogin() error         { f.setAccount("connected"); return nil }
func (f *testTelegram) SubmitPassword(string) error {
	return nil
}
func (f *testTelegram) CancelLogin() error { return nil }
func (f *testTelegram) SignOut(context.Context) error {
	f.setAccount("disconnected")
	return nil
}
func (f *testTelegram) Inspect(context.Context, MessageLink) (Media, error) {
	return f.media, nil
}
func (f *testTelegram) Close() {}
func (f *testTelegram) Download(ctx context.Context, _ MessageLink, _ Media, path string, progress func(int64)) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer file.Close()

	offset := partSize(path)
	f.mu.Lock()
	f.offsets = append(f.offsets, offset)
	pauseHere := len(f.offsets) == 1 && f.pauseFirst
	f.mu.Unlock()
	if pauseHere {
		const prefix = 8
		if _, err := file.WriteString(videoA[:prefix]); err != nil {
			return err
		}
		progress(prefix)
		close(f.reachedPart)
		<-ctx.Done()
		_ = file.Sync()
		return ctx.Err()
	}
	if _, err := file.WriteString(videoA[offset:]); err != nil {
		return err
	}
	progress(int64(len(videoA)))
	return file.Sync()
}

func (f *testTelegram) copyOffsets() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.offsets...)
}

func expectState(t *testing.T, q *queue, id, state string) Job {
	t.Helper()
	var last Job
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, job := range q.snapshot().Jobs {
			if job.ID == id {
				last = job
				if job.State == state {
					return job
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("wanted %q for %s, got %+v (%v)", state, id, last, last.Error)
	return Job{}
}

func expectCode(t *testing.T, err error, code string) {
	t.Helper()
	var failure *Failure
	if !errors.As(err, &failure) || failure == nil || failure.Code != code {
		t.Fatalf("wanted %q, got %v", code, err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestQueuePausesPersistsAndResumes(t *testing.T) {
	f := fakeTelegram("connected")
	f.pauseFirst = true
	f.reachedPart = make(chan struct{})
	data, destination := t.TempDir(), t.TempDir()
	original := filepath.Join(destination, "clip.mp4")
	if err := os.WriteFile(original, []byte("existing user file"), 0600); err != nil {
		t.Fatal(err)
	}

	q, err := openQueue(context.Background(), data, destination, "", f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.close)
	id, err := q.enqueue(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	_, err = q.enqueue(" t.me/s/SAMPLE_channel/00042?single ")
	expectCode(t, err, "duplicate")
	select {
	case <-f.reachedPart:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not reach the pause point")
	}
	if err := q.pause(id); err != nil {
		t.Fatal(err)
	}
	pausedJob := expectState(t, q, id, paused)
	if pausedJob.Downloaded != 8 || pausedJob.Speed != 0 {
		t.Fatalf("pause state: %+v", pausedJob)
	}
	q.close()

	q, err = openQueue(context.Background(), data, destination, "", f)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.close)
	pausedJob = expectState(t, q, id, paused)
	if pausedJob.Downloaded != 8 {
		t.Fatalf("restart did not remember the part: %+v", pausedJob)
	}
	if err := q.resume(id); err != nil {
		t.Fatal(err)
	}
	job := expectState(t, q, id, completed)
	if job.OutputPath != filepath.Join(destination, "clip (2).mp4") {
		t.Fatalf("unexpected output path %q", job.OutputPath)
	}
	if readFile(t, job.OutputPath) != videoA || readFile(t, original) != "existing user file" {
		t.Fatal("download altered existing data or wrote incomplete bytes")
	}
	offsets := f.copyOffsets()
	if len(offsets) != 2 || offsets[1] != 8 {
		t.Fatalf("expected offset to resume at 8, got %v", offsets)
	}
	thumb, err := q.thumbnail(id)
	if err != nil || thumb != jpeg {
		t.Fatalf("expected Telegram thumbnail, got %v, %v", thumb, err)
	}
	if output, err := q.revealPath(id); err != nil || output != job.OutputPath {
		t.Fatalf("revealPath = %q, %v", output, err)
	}
	if err := os.Remove(job.OutputPath); err != nil {
		t.Fatal(err)
	}
	_, err = q.revealPath(id)
	expectCode(t, err, "file_missing")
}

func TestWaitingSaveKeepsStageAndRetriesWithoutTelegram(t *testing.T) {
	f := fakeTelegram("disconnected")
	data, parent := t.TempDir(), t.TempDir()
	destination := filepath.Join(parent, "unavailable")
	q, err := openQueue(context.Background(), data, destination, "", f)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()

	id, err := q.enqueue(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	expectState(t, q, id, queued)
	f.setAccount("connected")
	q.notify()
	waiting := expectState(t, q, id, waitingSave)
	expectCode(t, waiting.Error, "folder_unavailable")
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unavailable destination was unexpectedly created: %v", err)
	}
	if readFile(t, q.partPath(id)) != videoA {
		t.Fatal("completed data was lost after save failed")
	}
	f.setAccount("disconnected")
	if err := q.retry(id); err != nil {
		t.Fatal(err)
	}
	expectState(t, q, id, waitingSave)
	other := filepath.Join(parent, "new destination")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if err := q.saveElsewhere(id, other); err != nil {
		t.Fatal(err)
	}
	finished := expectState(t, q, id, completed)
	if finished.Folder != other || readFile(t, finished.OutputPath) != videoA {
		t.Fatalf("wrong output: %+v", finished)
	}
	if offsets := f.copyOffsets(); len(offsets) != 1 {
		t.Fatalf("redownloaded unnecessarily, transfers: %v", offsets)
	}
}

func TestSpoilersSuppressThumbnails(t *testing.T) {
	f := fakeTelegram("connected")
	f.media.HideThumbnail = true
	q, err := openQueue(context.Background(), t.TempDir(), t.TempDir(), "", f)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	id, err := q.enqueue(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	job := expectState(t, q, id, completed)
	thumb, err := q.thumbnail(id)
	if err != nil || thumb != nil || job.HasThumbnail {
		t.Fatalf("unexpected spoiler thumbnail: %v %v", thumb, err)
	}
}
