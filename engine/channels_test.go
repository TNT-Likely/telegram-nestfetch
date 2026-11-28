// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

func TestChannelSelectors(t *testing.T) {
	for raw, wanted := range map[string]string{
		"@Telegram":                      "@telegram",
		" https://t.me/s/Telegram/ ":     "@telegram",
		"t.me/Telegram/28?single":        "@telegram",
		"https://t.me/c/1400012345/6/12": "1400012345",
		"https://t.me/c/1400012345":      "1400012345",
		"-1001400012345":                 "1400012345",
		"1400012345":                     "1400012345",
	} {
		got, err := parseChannelSelector(raw)
		if err != nil || got != wanted {
			t.Errorf("%q -> %q, %v", raw, got, err)
		}
	}
	for _, raw := range []string{
		"", "https://t.me/+invite", "https://t.me/joinchat", "https://t.me/login",
		"https://t.me.attacker.example/channel", "https://t.me/ch%61nnel", "https://t.me/Telegram?other=1",
		"@123", "https://t.me/telegram/23?comment=789",
	} {
		_, err := parseChannelSelector(raw)
		expectCode(t, err, "invalid_channel")
	}
}

func TestTDLChannelPageUsesDatePeerAndExclusiveCursor(t *testing.T) {
	const from = 1710000000
	raw := func(id, date int, peer int64, restricted bool) map[string]any {
		msg := &tg.Message{
			ID: id, Date: date, Noforwards: restricted, PeerID: &tg.PeerChannel{ChannelID: peer},
			Media: &tg.MessageMediaDocument{Document: &tg.Document{
				ID: int64(id) * 100, Size: 123, MimeType: "application/pdf",
				Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "Notes.pdf"}},
			}},
		}
		return map[string]any{"id": id, "type": "message", "file": "Notes.pdf", "raw": msg}
	}

	pageData, err := json.Marshal(map[string]any{
		"id": tdlFixtureChannel, "nextBefore": 10, "username": "sample_channel",
		"messages": []any{
			raw(19, from, tdlFixtureChannel, false), // too new for the cursor
			raw(18, from-1, tdlFixtureChannel, false),
			raw(17, from+1, tdlFixtureChannel, false),
			raw(16, from+1, tdlFixtureChannel, true),
			raw(15, from+2, tdlFixtureChannel+1, false),
			raw(14, from+3, tdlFixtureChannel, false),
			raw(14, from+3, tdlFixtureChannel, false),
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	page, err := decodeTDLChannelPage(pageData, channelQuery{
		Channel: "@sample_channel", From: from, To: from + 3, Before: 19,
	})
	if err != nil || page.NextBefore != 10 || len(page.Items) != 2 {
		t.Fatalf("unexpected page: %+v %v", page, err)
	}
	for i, expected := range []string{"https://t.me/sample_channel/17", "https://t.me/sample_channel/14"} {
		item := page.Items[i]
		if item.URL != expected || item.Kind != MediaFile || item.Name != "Notes.pdf" || item.Size != 123 {
			t.Fatalf("unexpected attachment: %+v", item)
		}
	}
}

func TestBatchQueueDeduplicatesAndStoresAtomically(t *testing.T) {
	p := fakeTelegram("disconnected")
	q, err := openQueue(context.Background(), t.TempDir(), t.TempDir(), "", p)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()

	if _, err := q.enqueue(mediaURL); err != nil {
		t.Fatal(err)
	}
	other := "https://t.me/c/100003/7"
	batch, err := q.enqueueBatch([]string{mediaURL, other, "https://t.me/c/100003/0007?single"})
	if err != nil || batch != (batchResult{Added: 1, Duplicates: 2}) {
		t.Fatalf("batch result: %+v, %v", batch, err)
	}
	_, err = q.enqueueBatch([]string{"https://t.me/c/100003/9", "https://example.com/c/100003/8"})
	expectCode(t, err, "invalid_link")
	before := q.snapshot()
	if len(before.Jobs) != 2 || before.Jobs[0].URL != other {
		t.Fatalf("batch persisted partial or duplicate records: %+v", before.Jobs)
	}
	for _, job := range before.Jobs {
		if job.Folder != before.Settings.Folder {
			t.Fatalf("batch lost the selected folder: %s", job.Folder)
		}
	}
}

type testTelegramPreview struct {
	*testTelegram
	stopped chan struct{}
}

func (p *testTelegramPreview) Preview(ctx context.Context, _ MessageLink, ready func(string), report func(int64, time.Duration)) error {
	ready("http://127.0.0.1:32000/preview/0148d1c53d37fb4b414d199a371fd586434ad8698aa0c5995e38c380c8d671e2")
	report(256*1024, 250*time.Millisecond)
	<-ctx.Done()
	close(p.stopped)
	return ctx.Err()
}

func TestPreviewCancellationReleasesQueuedDownloads(t *testing.T) {
	backend := &testTelegramPreview{testTelegram: fakeTelegram("connected"), stopped: make(chan struct{})}
	q, err := openQueue(context.Background(), t.TempDir(), t.TempDir(), "", backend)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()

	previewID, err := q.startPreview(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for {
		status, err := q.mediaTaskStatus("preview", previewID)
		if err != nil {
			t.Fatal(err)
		}
		if status.State == "ready" && status.URL != "" && status.BytesRead > 0 {
			if status.BytesRead != 256*1024 || status.Speed != 1024*1024 {
				t.Fatalf("unexpected preview throughput: %+v", status)
			}
			break
		}
		if status.State == "failed" || time.Now().After(deadline) {
			t.Fatalf("preview did not become ready: %+v", status)
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := q.enqueue(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	expectState(t, q, job, queued)

	q.cancelTelegramTask(previewID, true)
	select {
	case <-backend.stopped:
	case <-time.After(4 * time.Second):
		t.Fatal("preview did not stop")
	}
	_, err = q.mediaTaskStatus("preview", previewID)
	expectCode(t, err, "not_found")
	complete := expectState(t, q, job, completed)
	if filepath.Base(complete.OutputPath) != "clip.mp4" {
		t.Fatalf("queued download did not save after playback closed: %+v", complete)
	}
}

func TestInspectReturnsLinkMetadataAndDuplicateStatus(t *testing.T) {
	backend := fakeTelegram("connected")
	q, err := openQueue(context.Background(), t.TempDir(), t.TempDir(), "", backend)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()

	if _, err := q.startInspect("https://example.com/not-telegram"); err == nil {
		t.Fatal("expected invalid_link error")
	} else {
		expectCode(t, err, "invalid_link")
	}

	readInspect := func(raw string) *linkPreview {
		t.Helper()
		id, err := q.startInspect(raw)
		if err != nil {
			t.Fatal(err)
		}
		deadline := time.Now().Add(4 * time.Second)
		for {
			status, err := q.mediaTaskStatus("inspect", id)
			if err != nil {
				t.Fatal(err)
			}
			if status.State == "ready" {
				q.cancelTelegramTask(id, false)
				return status.Preview
			}
			if status.State == "failed" || time.Now().After(deadline) {
				t.Fatalf("inspect did not succeed: %+v", status)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	preview := readInspect(mediaURL + "?single")
	if preview == nil || preview.URL != mediaURL || preview.Name != "clip.mp4" || preview.Title != "Clip" ||
		preview.Source != "@sample" || preview.Kind != MediaVideo || preview.Size != int64(len(videoA)) ||
		preview.Duration != 6.5 || preview.Thumbnail != jpeg || preview.Duplicate {
		t.Fatalf("unexpected inspect preview: %+v", preview)
	}

	job, err := q.enqueue(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	expectState(t, q, job, completed)

	duplicate := readInspect(mediaURL)
	if duplicate == nil || !duplicate.Duplicate {
		t.Fatalf("expected duplicate flag for already downloaded message: %+v", duplicate)
	}

	backend.setAccount("disconnected")
	if _, err := q.startInspect(mediaURL); err == nil {
		t.Fatal("expected login_required error when disconnected")
	} else {
		expectCode(t, err, "login_required")
	}
}
