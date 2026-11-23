// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDesktopBackendSelection(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(t.TempDir(), "tdl")
	defaults := APIConfig{APIID: 100001, APIHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	check := func(config APIConfig, state, backend string, preconfigured bool) {
		t.Helper()
		provider, err := desktopTelegram(dir, binary, config)
		if err != nil {
			t.Fatal(err)
		}
		defer provider.Close()
		status := provider.Status()
		if status.State != state || status.Backend != backend || status.Preconfigured != preconfigured {
			t.Fatalf("got state=%s backend=%s preconfigured=%t", status.State, status.Backend, status.Preconfigured)
		}
	}
	check(APIConfig{}, "setup", "", false)
	if err := os.WriteFile(binary, []byte("test fixture"), 0700); err != nil {
		t.Fatal(err)
	}
	check(APIConfig{}, "disconnected", "tdl", true)
	check(defaults, "disconnected", "", true)

	native, err := newTelegram(dir, APIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	if err := native.Configure(defaults); err != nil {
		t.Fatal(err)
	}
	check(APIConfig{}, "disconnected", "", false)
}

func TestReopenKeepsTopicAndCleansInterruptedTDLTransfer(t *testing.T) {
	data, dest := t.TempDir(), t.TempDir()
	tg := fakeTelegram("disconnected")
	q, err := openQueue(context.Background(), data, dest, "", tg)
	if err != nil {
		t.Fatal(err)
	}
	id, err := q.enqueue("https://t.me/sample_channel/7/42")
	q.close()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(q.partPath(id))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	leftover, err := os.MkdirTemp(dir, ".tdl-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(leftover, "nestfetch-video.tmp"), []byte("interrupted"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(q.partPath(id), []byte("original part"), 0600); err != nil {
		t.Fatal(err)
	}
	q, err = openQueue(context.Background(), data, dest, "", tg)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if info, err := os.Stat(leftover); !os.IsNotExist(err) || info != nil {
		t.Fatal("interrupted tdl work directory was kept")
	}
	if part, err := os.ReadFile(q.partPath(id)); err != nil || string(part) != "original part" {
		t.Fatal("queue's canonical part was lost")
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if job := q.byIDLocked(id); job == nil || job.TopicID != 7 || job.URL != mediaURL {
		t.Fatal("queue did not retain the topic alongside the canonical message link")
	}
}
