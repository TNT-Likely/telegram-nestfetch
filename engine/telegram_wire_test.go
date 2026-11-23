// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	imagejpeg "image/jpeg"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/gotd/td/tgtest"
	"github.com/gotd/td/tgtest/cluster"
)

func TestTelegramWireLoginCoverAndResumedTransfer(t *testing.T) {
	const (
		channelID  = int64(1001)
		documentID = int64(3001)
		coverID    = int64(4001)
		messageID  = 11
	)
	user := &tg.User{ID: 100, Self: true}
	user.SetUsername("nestfetch_test")
	channel := &tg.Channel{ID: channelID, Title: "Synthetic QA channel", Photo: &tg.ChatPhotoEmpty{}}
	channel.SetAccessHash(222)
	channel.SetUsername("nestfetch_test_channel")
	fileBytes := make([]byte, 21*1024*1024+237)
	for i := range fileBytes {
		fileBytes[i] = byte((i + (i/telegramPartSize)*7) % 251)
	}
	var jpegCover bytes.Buffer
	coverImage := image.NewRGBA(image.Rect(0, 0, 4, 3))
	coverImage.SetRGBA(0, 0, color.RGBA{R: 240, A: 255})
	if err := imagejpeg.Encode(&jpegCover, coverImage, nil); err != nil {
		t.Fatal(err)
	}
	attachment := &tg.MessageMediaDocument{Video: true}
	attachment.SetDocument(&tg.Document{
		ID: documentID, AccessHash: 333, FileReference: []byte("fixture-document"),
		MimeType: "video/mp4", Size: int64(len(fileBytes)), DCID: 2,
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeFilename{FileName: "fixture.mp4"},
			&tg.DocumentAttributeVideo{Duration: 16.5, W: 1280, H: 720},
		},
	})
	attachment.SetVideoCover(&tg.Photo{
		ID: coverID, AccessHash: 444, FileReference: []byte("fixture-cover"), DCID: 2,
		Sizes: []tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "m", W: 4, H: 3, Size: jpegCover.Len()},
		},
	})
	message := &tg.Message{ID: messageID, PeerID: &tg.PeerChannel{ChannelID: channelID}}
	message.SetMedia(attachment)

	dc := cluster.NewCluster(cluster.Options{})
	dispatch := dc.Dispatch(2, "nestfetch-fixture")
	var issued atomic.Int32
	var confirmed atomic.Bool
	var firstVideoOffset atomic.Int64
	firstVideoOffset.Store(-1)
	dispatch.HandleFunc(tg.AuthExportLoginTokenRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		if confirmed.Load() {
			return server.SendResult(req, &tg.AuthLoginTokenSuccess{Authorization: &tg.AuthAuthorization{User: user}})
		}
		token := []byte(fmt.Sprintf("nestfetch-fixture-%d", issued.Add(1)))
		return server.SendResult(req, &tg.AuthLoginToken{Expires: int(time.Now().Add(time.Second).Unix()), Token: token})
	})
	dispatch.HandleFunc(tg.UsersGetUsersRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		if !confirmed.Load() {
			return server.SendErr(req, tgerr.New(401, "AUTH_KEY_UNREGISTERED"))
		}
		return server.SendVector(req, user)
	})
	dispatch.Result(tg.UpdatesGetStateRequestTypeID, &tg.UpdatesState{})
	dispatch.Result(tg.ContactsResolveUsernameRequestTypeID, &tg.ContactsResolvedPeer{
		Peer: &tg.PeerChannel{ChannelID: channelID}, Chats: []tg.ChatClass{channel},
	})
	dispatch.Result(tg.ChannelsGetMessagesRequestTypeID, &tg.MessagesMessages{
		Messages: []tg.MessageClass{message}, Chats: []tg.ChatClass{channel},
	})
	dispatch.HandleFunc(tg.MessagesGetDialogsRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var request tg.MessagesGetDialogsRequest
		if err := request.Decode(req.Buf); err != nil {
			return err
		}
		if request.FolderID != 1 {
			return server.SendResult(req, &tg.MessagesDialogs{})
		}
		dialog := &tg.Dialog{Peer: &tg.PeerChannel{ChannelID: channelID}, TopMessage: messageID}
		return server.SendResult(req, &tg.MessagesDialogs{
			Dialogs: []tg.DialogClass{dialog}, Chats: []tg.ChatClass{channel},
			Messages: []tg.MessageClass{message},
		})
	})
	dispatch.HandleFunc(tg.UploadGetFileRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		var file tg.UploadGetFileRequest
		if err := file.Decode(req.Buf); err != nil {
			return err
		}
		switch location := file.Location.(type) {
		case *tg.InputPhotoFileLocation:
			if location.ID != coverID || location.ThumbSize != "m" {
				return server.SendErr(req, tgerr.New(400, "FILE_ID_INVALID"))
			}
			return server.SendResult(req, &tg.UploadFile{Type: &tg.StorageFileJpeg{}, Bytes: jpegCover.Bytes()})
		case *tg.InputDocumentFileLocation:
			if location.ID != documentID || location.ThumbSize != "" || file.Offset < 0 || file.Offset > int64(len(fileBytes)) {
				return server.SendErr(req, tgerr.New(400, "FILE_ID_INVALID"))
			}
			firstVideoOffset.CompareAndSwap(-1, file.Offset)
			end := min(file.Offset+int64(file.Limit), int64(len(fileBytes)))
			return server.SendResult(req, &tg.UploadFile{Type: &tg.StorageFileMp4{}, Bytes: fileBytes[file.Offset:end]})
		}
		return server.SendErr(req, tgerr.New(400, "FILE_ID_INVALID"))
	})
	dispatch.HandleFunc(tg.AuthLogOutRequestTypeID, func(server *tgtest.Server, req *tgtest.Request) error {
		confirmed.Store(false)
		return server.SendResult(req, &tg.AuthLoggedOut{})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	serverDone := make(chan error, 1)
	go func() { serverDone <- dc.Up(ctx) }()
	defer func() {
		cancel()
		<-serverDone
	}()
	select {
	case <-dc.Ready():
	case <-ctx.Done():
		t.Fatal("local MTProto fixture didn't start")
	}
	temp := t.TempDir()
	local, err := newTelegram(temp, APIConfig{})
	if err != nil {
		t.Fatal(err)
	}
	p := local.(*telegramProvider)
	defer p.Close()
	if err := p.Configure(APIConfig{APIID: telegram.TestAppID, APIHash: telegram.TestAppHash}); err != nil {
		t.Fatal(err)
	}

	start := func(restore bool) {
		update := tg.NewUpdateDispatcher()
		loggedIn := qrlogin.OnLoginToken(&update)
		options := telegram.Options{
			PublicKeys: dc.Keys(), DC: 2, DCList: dc.List(), Resolver: dc.Resolver(),
			UpdateHandler: update, SessionStorage: p.storage, AllowCDN: true,
		}
		runCtx, stop := context.WithCancelCause(ctx)
		run := &telegramRun{
			ctx: runCtx, cancel: func() { stop(nil) }, done: make(chan struct{}), passwords: make(chan string, 1),
			client: telegram.NewClient(telegram.TestAppID, telegram.TestAppHash, options),
		}
		p.mu.Lock()
		p.run = run
		p.account = Account{State: "starting"}
		p.mu.Unlock()
		go p.runClient(run, loggedIn, restore, stop)
	}
	start(false)
	first := waitForTelegramAccount(t, ctx, p, func(a Account) bool { return a.State == "qr" && a.QRURL != "" })
	waitForTelegramAccount(t, ctx, p, func(a Account) bool {
		return a.State == "qr" && a.QRURL != "" && a.QRURL != first.QRURL
	})
	confirmed.Store(true)
	account := waitForTelegramAccount(t, ctx, p, func(a Account) bool { return a.State == "connected" })
	if account.Label != "@nestfetch_test" || account.QRURL != "" {
		t.Fatal("authorization identification or private QR state was wrong")
	}

	link := MessageLink{Username: "nestfetch_test_channel", MessageID: messageID}
	media, err := p.Inspect(ctx, link)
	if err != nil {
		t.Fatal(err)
	}
	if media.ID != documentID || media.Bytes != int64(len(fileBytes)) || media.Duration != 16.5 ||
		media.Thumbnail != telegramThumbnailURL(jpegCover.Bytes()) {
		t.Fatalf("unexpected inspection metadata: ID=%d Bytes=%d Duration=%v", media.ID, media.Bytes, media.Duration)
	}
	archived, err := p.Inspect(ctx, MessageLink{ChannelID: channelID, MessageID: messageID})
	if err != nil || archived.ID != media.ID || archived.Source != channel.Title {
		t.Fatalf("archived private link lookup failed: %v", err)
	}

	target := filepath.Join(temp, "download.part")
	if err := os.WriteFile(target, fileBytes[:telegramPartSize+37], 0600); err != nil {
		t.Fatal(err)
	}
	var finalProgress int64
	if err := p.Download(ctx, link, media, target, func(bytes int64) { finalProgress = bytes }); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, fileBytes) {
		t.Fatalf("download bytes differed from fixture: %v", err)
	}
	if firstVideoOffset.Load() != telegramPartSize || finalProgress != int64(len(fileBytes)) {
		t.Fatalf("incorrect resume or progress: first offset %d, final size %d", firstVideoOffset.Load(), finalProgress)
	}
	p.Close()
	start(true)
	waitForTelegramAccount(t, ctx, p, func(a Account) bool { return a.State == "connected" })
	if err := p.SignOut(ctx); err != nil || confirmed.Load() {
		t.Fatalf("local fixture sign-out failed: %v", err)
	}
	if p.Status().State != "disconnected" || p.storage.exists() {
		t.Fatal("sign-out left local authorization active")
	}
}

func waitForTelegramAccount(t *testing.T, ctx context.Context, p Telegram, accept func(Account) bool) Account {
	t.Helper()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		account := p.Status()
		if account.State == "error" {
			t.Fatalf("local Telegram provider returned %v", account.Error)
		}
		if accept(account) {
			return account
		}
		select {
		case <-ctx.Done():
			t.Fatalf("timed out waiting for account state from %s", account.State)
		case <-ticker.C:
		}
	}
}
