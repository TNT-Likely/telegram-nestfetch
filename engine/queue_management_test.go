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

func TestCompletedJobCanBeHiddenWithoutDroppingDeduplication(t *testing.T) {
	data, destination := t.TempDir(), t.TempDir()
	provider := fakeTelegram("connected")
	q, err := openQueue(context.Background(), data, destination, "", provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.close)
	id, err := q.enqueue(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	job := expectState(t, q, id, completed)
	if err := q.setJobHidden(id, true); err != nil {
		t.Fatal(err)
	}
	expectCode(t, q.removeJob(context.Background(), id, true), "invalid_state")
	q.close()

	q, err = openQueue(context.Background(), data, destination, "", provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.close)
	if !expectState(t, q, id, completed).Hidden {
		t.Fatal("hidden status was lost on restart")
	}
	_, err = q.enqueue(mediaURL)
	expectCode(t, err, "duplicate")
	if path, err := q.revealPath(id); err != nil || path != job.OutputPath || readFile(t, path) != videoA {
		t.Fatalf("hidden download did not retain its saved file: %v", err)
	}
	if err := q.setJobHidden(id, false); err != nil {
		t.Fatal(err)
	}
	if expectState(t, q, id, completed).Hidden {
		t.Fatal("restored job is still hidden")
	}
}

type removalTestTelegram struct {
	*testTelegram
	started chan struct{}
	stopped chan struct{}
	release chan struct{}
}

func (f *removalTestTelegram) Download(ctx context.Context, _ MessageLink, _ Media, path string, progress func(int64)) error {
	if err := os.WriteFile(path, []byte(videoA[:8]), 0600); err != nil {
		return err
	}
	progress(8)
	close(f.started)
	<-ctx.Done()
	close(f.stopped)
	<-f.release
	return ctx.Err()
}

func TestRemovingActiveJobWaitsForDownloadToStop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	provider := &removalTestTelegram{
		testTelegram: fakeTelegram("connected"), started: make(chan struct{}),
		stopped: make(chan struct{}), release: make(chan struct{}),
	}
	destination := t.TempDir()
	existing := filepath.Join(destination, "clip.mp4")
	if err := os.WriteFile(existing, []byte(videoB), 0600); err != nil {
		t.Fatal(err)
	}
	q, err := openQueue(ctx, t.TempDir(), destination, "", provider)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	release := sync.OnceFunc(func() { close(provider.release) })
	defer release()
	id, err := q.enqueue(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-provider.started:
	case <-ctx.Done():
		t.Fatal("download did not start")
	}
	removed := make(chan error, 1)
	go func() { removed <- q.removeJob(ctx, id, false) }()
	select {
	case <-provider.stopped:
	case <-ctx.Done():
		t.Fatal("download did not receive cancellation")
	}
	if readFile(t, q.partPath(id)) != videoA[:8] {
		t.Fatal("part disappeared while the download worker was still stopping")
	}
	if len(q.snapshot().Jobs) != 1 || q.snapshot().Jobs[0].State != removing {
		t.Fatal("task was dropped before its worker stopped")
	}
	release()
	if err := <-removed; err != nil {
		t.Fatal(err)
	}
	if len(q.snapshot().Jobs) != 0 {
		t.Fatal("completed removal retained the task")
	}
	assertJobCacheRemoved(t, q, id)
	if readFile(t, existing) != videoB {
		t.Fatal("removal changed a file in the selected folder")
	}
}

func TestRemovalOfCompleteStagedFileRequiresConfirmationAndRecovers(t *testing.T) {
	data := t.TempDir()
	destination := filepath.Join(t.TempDir(), "offline")
	provider := fakeTelegram("connected")
	q, err := openQueue(context.Background(), data, destination, "", provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.close)
	id, err := q.enqueue(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	if !expectState(t, q, id, waitingSave).TransferDone {
		t.Fatal("the queue omitted the complete staging state")
	}
	expectCode(t, q.removeJob(context.Background(), id, false), "discard_staged_required")
	if readFile(t, q.partPath(id)) != videoA {
		t.Fatal("unconfirmed removal changed the complete stage")
	}
	done, err := q.beginRemoval(id, true)
	if err != nil {
		t.Fatal(err)
	}
	if done != nil {
		<-done
	}
	q.close()

	q, err = openQueue(context.Background(), data, destination, "", provider)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(q.close)
	if len(q.snapshot().Jobs) != 0 {
		t.Fatal("restart did not finish the committed removal")
	}
	assertJobCacheRemoved(t, q, id)
	if _, err := os.Stat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removal touched the unavailable destination: %v", err)
	}
	if _, err := q.enqueue(mediaURL); err != nil {
		t.Fatalf("discarded job prevented queueing again: %v", err)
	}
}

func assertJobCacheRemoved(t *testing.T, q *queue, id string) {
	t.Helper()
	for _, file := range []string{filepath.Dir(q.partPath(id)), q.thumbnailPath(id)} {
		if _, err := os.Lstat(file); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("job cache remained at %q: %v", file, err)
		}
	}
}
