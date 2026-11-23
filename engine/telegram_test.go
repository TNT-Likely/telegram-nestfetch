// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gotd/td/session"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

func TestTelegramConfigIsLocalAndDoesNotSignIn(t *testing.T) {
	root := t.TempDir()
	provider, err := newTelegram(root, APIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()

	config := APIConfig{APIID: 100001, APIHash: strings.Repeat("a", 32)}
	if err := provider.Configure(APIConfig{APIID: config.APIID, APIHash: "bad"}); err == nil {
		t.Fatal("invalid hash accepted")
	}
	if err := provider.Configure(config); err != nil {
		t.Fatal(err)
	}
	if account := provider.Status(); account.State != "disconnected" || account.QRURL != "" {
		t.Fatalf("configure unexpectedly authenticated: %+v", account)
	}

	privateDir := filepath.Join(root, "telegram")
	configPath := filepath.Join(privateDir, "api.json")
	info, err := os.Stat(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("configuration is not private: mode %o", info.Mode().Perm())
	}
	if _, err := os.Stat(filepath.Join(privateDir, "session.json")); !os.IsNotExist(err) {
		t.Fatal("configuration unexpectedly created a session")
	}
	reloaded, err := newTelegram(root, APIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if reloaded.Status().State != "disconnected" {
		t.Fatal("reloading did not restore configuration")
	}
}

func TestTelegramReleaseDefaultsAndLocalOverride(t *testing.T) {
	root := t.TempDir()
	defaults := APIConfig{APIID: 100001, APIHash: strings.Repeat("a", 32)}
	provider, err := newTelegram(root, defaults)
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	account := provider.Status()
	if account.State != "disconnected" || !account.Preconfigured {
		t.Fatalf("expected a release install ready to sign in: %+v", account)
	}
	p := provider.(*telegramProvider)
	if p.config != defaults || filepath.Base(p.storage.path) != "session-app-100001.json" {
		t.Fatal("release session is not tied to the built-in Telegram application")
	}
	if _, err := os.Stat(p.apiPath); !os.IsNotExist(err) {
		t.Fatal("starting a release install wrote application credentials to the user settings")
	}

	custom := APIConfig{APIID: 100002, APIHash: strings.Repeat("b", 32)}
	if err := provider.Configure(custom); err != nil {
		t.Fatal(err)
	}
	reloaded, err := newTelegram(root, defaults)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if reloaded.Status().Preconfigured || reloaded.(*telegramProvider).config != custom ||
		filepath.Base(reloaded.(*telegramProvider).storage.path) != "session.json" {
		t.Fatal("the release defaults replaced existing local application settings")
	}
}

func TestTelegramSessionStorageReplacement(t *testing.T) {
	storage := &telegramStorage{path: filepath.Join(t.TempDir(), "session.json")}
	ctx := context.Background()
	if _, err := storage.LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("empty session: %v", err)
	}
	for _, value := range []string{"previous-session", "new-session"} {
		if err := storage.StoreSession(ctx, []byte(value)); err != nil {
			t.Fatal(err)
		}
		if got, err := storage.LoadSession(ctx); err != nil || string(got) != value {
			t.Fatalf("latest session not loaded: %v", err)
		}
	}
	if err := storage.StoreSession(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := storage.LoadSession(ctx); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("session remained after logout: %v", err)
	}
}

func TestTelegramCancelRestoringPreservesSession(t *testing.T) {
	local, err := newTelegram(t.TempDir(), APIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	p := local.(*telegramProvider)
	defer p.Close()
	if err := p.storage.StoreSession(context.Background(), []byte("fixture-session")); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	close(done)
	p.run = &telegramRun{hadSession: true, cancel: func() {}, done: done}
	p.account = Account{State: "starting"}

	if err := p.CancelLogin(); err != nil {
		t.Fatal(err)
	}
	if !p.storage.exists() {
		t.Fatal("cancelling validation of a stored session deleted it")
	}
}

func TestTelegramDocumentTransferUsesExactVideoOnly(t *testing.T) {
	ch := &tg.InputChannel{ChannelID: 123}
	doc := tg.Document{ID: 9223372036854775000, Size: 42 * 1024 * 1024, MimeType: "video/mp4"}
	doc.Attributes = []tg.DocumentAttributeClass{
		&tg.DocumentAttributeFilename{FileName: "../海岸.mp4"},
		&tg.DocumentAttributeVideo{Duration: 9.5},
	}
	cover := &tg.Photo{ID: 1234}
	attachment := &tg.MessageMediaDocument{Document: &doc, VideoCover: cover}
	message := tg.Message{ID: 41, Media: attachment}
	got, err := telegramVideoFromMessage(&message, ch, "频道")
	if err != nil {
		t.Fatal(err)
	}
	if got.doc != &doc || got.cover != cover || got.media.ID != doc.ID ||
		got.media.Name != "海岸.mp4" || got.media.Title != "海岸" ||
		got.media.Source != "频道" || got.media.Bytes != doc.Size || got.media.Duration != 9.5 {
		t.Fatalf("unexpected video metadata: %+v", got.media)
	}

	for _, msg := range []tg.Message{
		{Noforwards: true, Media: attachment},
		{TTLPeriod: 60, Media: attachment},
		{Media: &tg.MessageMediaDocument{TTLSeconds: 1, Document: &doc}},
	} {
		_, err := telegramVideoFromMessage(&msg, ch, "")
		if mapTelegramFailure(err).Code != "protected_media" {
			t.Fatalf("protected video passed: %v", err)
		}
	}

	photo := tg.Message{Media: &tg.MessageMediaPhoto{Photo: cover}}
	_, err = telegramVideoFromMessage(&photo, ch, "")
	if mapTelegramFailure(err).Code != "no_video" {
		t.Fatalf("non-video accepted: %v", err)
	}
}

func TestTelegramPartialSkipsWrittenPartsAndBoundsTotal(t *testing.T) {
	target := filepath.Join(t.TempDir(), "download.part")
	tail := 127
	total := int64(telegramPartSize + 2*tail)
	firstPart := bytes.Repeat([]byte{7}, telegramPartSize)
	data := append(append([]byte(nil), firstPart...), bytes.Repeat([]byte{8}, tail)...)
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}

	file, offset, err := openTelegramPartial(target, total)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if offset != telegramPartSize {
		t.Fatalf("resume offset was %d", offset)
	}
	writer := telegramVideoWriter{file: file, remaining: total - offset}
	if _, err := writer.Write(bytes.Repeat([]byte{9}, 2*tail+1)); mapTelegramFailure(err).Code != "download_incomplete" {
		t.Fatalf("bytes beyond end accepted: %v", err)
	}
	finalPart := bytes.Repeat([]byte{9}, 2*tail)
	if _, err := writer.Write(finalPart); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got[:offset], firstPart) || !bytes.Equal(got[offset:], finalPart) {
		t.Fatalf("resumed output differs from source: %v", err)
	}
	reopened, done, err := openTelegramPartial(target, total)
	if err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	if done != total {
		t.Fatalf("complete file was truncated for resume: %d/%d", done, total)
	}
}

func TestTelegramThumbnailsAreBoundedAndPreferResolution(t *testing.T) {
	ranked := telegramThumbs([]tg.PhotoSizeClass{
		&tg.PhotoSize{Type: "s", W: 90, H: 90, Size: 4},
		&tg.PhotoSize{Type: "oversize", W: 1280, H: 960, Size: maxTelegramThumbnail + 1},
		&tg.PhotoSizeProgressive{Type: "m", W: 300, H: 200, Sizes: []int{4, 80}},
	})
	if len(ranked) != 2 || ranked[0].kind != "m" || ranked[0].bytes != 80 {
		t.Fatalf("unexpected thumbnail order: %+v", ranked)
	}
	if data := telegramThumbnailURL([]byte("<html>no</html>")); data != "" {
		t.Fatal("accepted non-image as thumbnail")
	}
	var thumbnail telegramThumbnailWriter
	if _, err := thumbnail.Write(make([]byte, maxTelegramThumbnail+1)); err == nil {
		t.Fatal("accepted oversized thumbnail")
	}
}

func TestTelegramFailuresUseStableCodes(t *testing.T) {
	cases := []struct {
		err  error
		code string
	}{
		{context.DeadlineExceeded, "telegram_network"},
		{tgerr.New(400, "API_ID_INVALID"), "telegram_credentials_invalid"},
		{tgerr.New(401, "SESSION_REVOKED"), "session_expired"},
		{tgerr.New(400, "CHANNEL_PRIVATE"), "access_denied"},
		{tgerr.New(400, "MESSAGE_ID_INVALID"), "message_not_found"},
		{tgerr.New(420, "FLOOD_WAIT_15"), "telegram_rate_limited"},
	}
	for _, tc := range cases {
		if got := mapTelegramFailure(tc.err); got.Code != tc.code || got.Message != "" {
			t.Errorf("%v: %+v instead of %q", tc.err, got, tc.code)
		}
	}
}
