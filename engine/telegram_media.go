// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/telegram/thumbnail"
	"github.com/gotd/td/tg"
)

const (
	// gotd's downloader requests 512 KiB aligned parts. WithOffset reuses
	// the contiguous prefix written by Stream; a partial part is fetched again.
	telegramPartSize     = 512 * 1024
	maxTelegramThumbnail = 384 * 1024
	telegramTransferIdle = 60 * time.Second
)

type telegramVideo struct {
	doc     *tg.Document
	cover   *tg.Photo
	media   Media
	channel *tg.InputChannel
}

func (p *telegramProvider) Inspect(parent context.Context, link MessageLink) (Media, error) {
	client, ctx, cleanup, err := p.sessionFor(parent)
	if err != nil {
		return Media{}, err
	}
	defer cleanup()

	lookup, cancel := context.WithTimeout(ctx, connectDeadline)
	video, err := findTelegramVideo(lookup, client.API(), link)
	cancel()
	if err != nil {
		return Media{}, telegramOperationError(ctx, err)
	}

	if !video.media.HideThumbnail {
		preview, stop := context.WithTimeout(ctx, 6*time.Second)
		video.media.Thumbnail = telegramThumbnail(preview, client, video)
		stop()
	}
	return video.media, ctx.Err()
}

func (p *telegramProvider) Download(parent context.Context, link MessageLink, media Media, target string, progress func(int64)) error {
	client, ctx, cleanup, err := p.sessionFor(parent)
	if err != nil {
		return err
	}
	defer cleanup()

	lookup, cancel := context.WithTimeout(ctx, connectDeadline)
	video, err := findTelegramVideo(lookup, client.API(), link)
	cancel()
	if err != nil {
		return telegramOperationError(ctx, err)
	}
	if video.doc.ID != media.ID || video.doc.Size != media.Bytes {
		return telegramFailure("media_changed")
	}

	file, offset, err := openTelegramPartial(target, media.Bytes)
	if err != nil {
		return err
	}
	defer file.Close()
	if progress != nil {
		progress(offset)
	}

	if offset < media.Bytes {
		if err := streamTelegramVideo(ctx, client, link, video, file, offset, progress); err != nil {
			return err
		}
	}

	info, err := file.Stat()
	if err != nil {
		return telegramFailure("disk_write_failed")
	}
	if info.Size() != media.Bytes {
		return telegramFailure("download_incomplete")
	}
	if err := file.Sync(); err != nil {
		return telegramFailure("disk_write_failed")
	}
	if err := file.Close(); err != nil {
		return telegramFailure("disk_write_failed")
	}
	return ctx.Err()
}

func openTelegramPartial(target string, total int64) (*os.File, int64, error) {
	if info, err := os.Lstat(target); err == nil && !info.Mode().IsRegular() {
		return nil, 0, telegramFailure("disk_write_failed")
	}
	file, err := os.OpenFile(target, os.O_RDWR|os.O_CREATE, 0600)
	if err != nil {
		return nil, 0, telegramFailure("disk_write_failed")
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		file.Close()
		return nil, 0, telegramFailure("disk_write_failed")
	}
	offset := info.Size()
	switch {
	case offset > total:
		offset = 0
	case offset < total:
		offset -= offset % telegramPartSize
	}
	if offset != info.Size() {
		if err := file.Truncate(offset); err != nil {
			file.Close()
			return nil, 0, telegramFailure("disk_write_failed")
		}
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		file.Close()
		return nil, 0, telegramFailure("disk_write_failed")
	}
	return file, offset, nil
}

func streamTelegramVideo(
	ctx context.Context, client *telegram.Client, link MessageLink, video telegramVideo,
	file *os.File, offset int64, progress func(int64),
) error {
	transfer, stop := context.WithCancelCause(ctx)
	defer stop(nil)
	timer := time.AfterFunc(telegramTransferIdle, func() { stop(context.DeadlineExceeded) })
	defer timer.Stop()

	invoker, err := client.FileDC(transfer, video.doc.DCID)
	if err != nil {
		return telegramOperationError(ctx, err)
	}
	dst := &telegramVideoWriter{file: file, remaining: video.doc.Size - offset}
	_, err = client.DownloadVia(invoker, documentLocation(video.doc, "")).
		WithSize(video.doc.Size).
		WithOffset(offset).
		WithRefresh(func(ctx context.Context) (tg.InputFileLocationClass, error) {
			updated, err := fetchTelegramVideo(ctx, client.API(), video.channel, link, video.media.Source)
			if err != nil {
				return nil, err
			}
			if updated.doc.ID != video.doc.ID || updated.doc.Size != video.doc.Size {
				return nil, telegramFailure("media_changed")
			}
			return documentLocation(updated.doc, ""), nil
		}).
		WithProgress(func(done, _ int64) {
			timer.Reset(telegramTransferIdle)
			if progress != nil {
				progress(done)
			}
		}).
		Stream(transfer, dst)
	if err != nil {
		if errors.Is(context.Cause(transfer), context.DeadlineExceeded) {
			return telegramFailure("telegram_network")
		}
		return telegramOperationError(ctx, err)
	}
	if dst.remaining != 0 {
		return telegramFailure("download_incomplete")
	}
	return nil
}

// Limit the accepted bytes to the exact document size Telegram advertised.
type telegramVideoWriter struct {
	file      io.Writer
	remaining int64
}

func (w *telegramVideoWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, telegramFailure("download_incomplete")
	}
	n, err := w.file.Write(data)
	w.remaining -= int64(n)
	if err != nil || n != len(data) {
		return n, telegramFailure("disk_write_failed")
	}
	return n, nil
}

func telegramOperationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return mapTelegramFailure(err)
}

func findTelegramVideo(ctx context.Context, api *tg.Client, link MessageLink) (telegramVideo, error) {
	channel, source, err := resolveTelegramChannel(ctx, api, link)
	if err != nil {
		return telegramVideo{}, err
	}
	return fetchTelegramVideo(ctx, api, channel, link, source)
}

func resolveTelegramChannel(ctx context.Context, api *tg.Client, link MessageLink) (*tg.InputChannel, string, error) {
	if link.Username != "" {
		entity, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: link.Username})
		if err != nil {
			return nil, "", err
		}
		peer, ok := entity.Peer.(*tg.PeerChannel)
		if !ok {
			return nil, "", telegramFailure("message_not_found")
		}
		for _, chat := range entity.Chats {
			if channel, ok := chat.(*tg.Channel); ok && channel.ID == peer.ChannelID {
				if channel.Noforwards {
					return nil, "", telegramFailure("protected_media")
				}
				return &tg.InputChannel{ChannelID: channel.ID, AccessHash: channel.AccessHash}, "@" + link.Username, nil
			}
		}
		return nil, "", telegramFailure("access_denied")
	}

	// Private post links contain a channel ID, but no access hash. Resolve
	// it through the authenticated user's own dialogs, including archived
	// groups. This neither joins nor marks any chat or post as read.
	builder := dialogs.NewQueryBuilder(api).GetDialogs()
	for _, folder := range []int{0, 1} {
		iter := builder.FolderID(folder).Iter()
		for iter.Next(ctx) {
			entry := iter.Value()
			peer, ok := entry.Peer.(*tg.InputPeerChannel)
			if !ok || peer.ChannelID != link.ChannelID {
				continue
			}
			title := ""
			if channel := entry.Entities.Channels()[peer.ChannelID]; channel != nil {
				if channel.Noforwards {
					return nil, "", telegramFailure("protected_media")
				}
				title = telegramDisplayText(channel.Title)
			}
			return &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}, title, nil
		}
		if err := iter.Err(); err != nil {
			return nil, "", err
		}
	}
	return nil, "", telegramFailure("access_denied")
}

func fetchTelegramVideo(
	ctx context.Context, api *tg.Client, channel *tg.InputChannel, link MessageLink, source string,
) (telegramVideo, error) {
	result, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: channel,
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: link.MessageID}},
	})
	if err != nil {
		return telegramVideo{}, err
	}
	messages, ok := result.AsModified()
	if !ok {
		return telegramVideo{}, telegramFailure("message_not_found")
	}
	for _, chat := range messages.GetChats() {
		c, ok := chat.(*tg.Channel)
		if !ok || c.ID != channel.ChannelID {
			continue
		}
		if c.Noforwards {
			return telegramVideo{}, telegramFailure("protected_media")
		}
		if source == "" {
			source = telegramDisplayText(c.Title)
		}
	}
	for _, item := range messages.GetMessages() {
		msg, ok := item.(*tg.Message)
		if !ok || msg.ID != link.MessageID {
			continue
		}
		if peer, ok := msg.PeerID.(*tg.PeerChannel); ok && peer.ChannelID == channel.ChannelID {
			return telegramVideoFromMessage(msg, channel, source)
		}
	}
	return telegramVideo{}, telegramFailure("message_not_found")
}

func telegramVideoFromMessage(message *tg.Message, channel *tg.InputChannel, source string) (telegramVideo, error) {
	if message.Noforwards || message.TTLPeriod != 0 {
		return telegramVideo{}, telegramFailure("protected_media")
	}
	attachment, ok := message.Media.(*tg.MessageMediaDocument)
	if !ok {
		return telegramVideo{}, telegramFailure("no_video")
	}
	if _, timed := attachment.GetTTLSeconds(); timed || attachment.TTLSeconds != 0 {
		return telegramVideo{}, telegramFailure("protected_media")
	}
	doc, ok := attachment.Document.(*tg.Document)
	if !ok || doc.ID == 0 || doc.Size <= 0 {
		return telegramVideo{}, telegramFailure("no_video")
	}

	name := ""
	duration := 0.0
	video := attachment.Video || attachment.Round || strings.HasPrefix(doc.MimeType, "video/")
	for _, attribute := range doc.Attributes {
		switch detail := attribute.(type) {
		case *tg.DocumentAttributeFilename:
			name = detail.FileName
		case *tg.DocumentAttributeVideo:
			video = true
			if detail.Duration > 0 && !math.IsInf(detail.Duration, 0) {
				duration = detail.Duration
			}
		}
	}
	if !video {
		return telegramVideo{}, telegramFailure("no_video")
	}

	name = telegramVideoFilename(name, doc.MimeType, message.ID)
	title := strings.TrimSuffix(name, path.Ext(name))
	cover, _ := attachment.VideoCover.(*tg.Photo)
	return telegramVideo{
		doc:     doc,
		cover:   cover,
		channel: channel,
		media: Media{
			ID: doc.ID, Name: name, Title: title, Source: source, Bytes: doc.Size,
			Duration: duration, HideThumbnail: attachment.Spoiler,
		},
	}, nil
}

func telegramVideoFilename(name, mime string, id int) string {
	name = telegramDisplayText(path.Base(strings.ReplaceAll(name, "\\", "/")))
	ext := path.Ext(name)
	switch strings.ToLower(ext) {
	case ".mp4", ".mov", ".m4v", ".webm", ".mkv", ".avi", ".wmv", ".mpeg", ".mpg", ".ogv", ".3gp", ".3g2", ".asf", ".m2ts", ".ts", ".flv", ".vob", ".rm", ".rmvb":
		if strings.TrimSuffix(name, ext) != "" {
			return name
		}
	}
	base := strings.Trim(strings.TrimSuffix(name, ext), " .")
	if base == "" {
		base = "video_" + strconv.Itoa(id)
	}
	switch mime {
	case "video/webm":
		return base + ".webm"
	case "video/quicktime":
		return base + ".mov"
	case "video/x-matroska":
		return base + ".mkv"
	case "video/x-msvideo":
		return base + ".avi"
	case "video/mpeg":
		return base + ".mpeg"
	case "video/x-ms-wmv":
		return base + ".wmv"
	case "video/ogg":
		return base + ".ogv"
	default:
		return base + ".mp4"
	}
}

func telegramDisplayText(text string) string {
	var output strings.Builder
	runes := 0
	for _, char := range text {
		if unicode.IsControl(char) || unicode.Is(unicode.Bidi_Control, char) {
			continue
		}
		output.WriteRune(char)
		runes++
		if runes == 180 {
			break
		}
	}
	return strings.TrimSpace(output.String())
}

func documentLocation(doc *tg.Document, size string) *tg.InputDocumentFileLocation {
	return &tg.InputDocumentFileLocation{
		ID:            doc.ID,
		AccessHash:    doc.AccessHash,
		FileReference: doc.FileReference,
		ThumbSize:     size,
	}
}

type telegramThumb struct {
	kind   string
	inline []byte
	bytes  int
	pixels int
}

func telegramThumbs(sizes []tg.PhotoSizeClass) []telegramThumb {
	candidates := make([]telegramThumb, 0, len(sizes))
	for _, rank := range sizes {
		var w, h int
		var thumb telegramThumb
		switch size := rank.(type) {
		case *tg.PhotoCachedSize:
			w, h = size.W, size.H
			thumb = telegramThumb{inline: size.Bytes, bytes: len(size.Bytes)}
		case *tg.PhotoSize:
			w, h = size.W, size.H
			thumb = telegramThumb{kind: size.Type, bytes: size.Size}
		case *tg.PhotoSizeProgressive:
			w, h = size.W, size.H
			if len(size.Sizes) == 0 {
				continue
			}
			thumb = telegramThumb{kind: size.Type, bytes: size.Sizes[len(size.Sizes)-1]}
		case *tg.PhotoStrippedSize:
			if len(size.Bytes) >= 3 && len(size.Bytes) <= 1024 {
				if data, err := thumbnail.Expand(size.Bytes); err == nil {
					candidates = append(candidates, telegramThumb{inline: data, bytes: len(data), pixels: 1})
				}
			}
			continue
		default:
			continue
		}
		if w <= 0 || h <= 0 || w > 1600 || h > 1600 || thumb.bytes <= 0 || thumb.bytes > maxTelegramThumbnail {
			continue
		}
		thumb.pixels = w * h
		candidates = append(candidates, thumb)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].pixels > candidates[j].pixels })
	return candidates
}

func telegramThumbnail(ctx context.Context, client *telegram.Client, video telegramVideo) string {
	if video.cover != nil {
		cover := video.cover
		if image := fetchTelegramThumbnail(ctx, client, cover.DCID, telegramThumbs(cover.Sizes), func(kind string) tg.InputFileLocationClass {
			return &tg.InputPhotoFileLocation{ID: cover.ID, AccessHash: cover.AccessHash, FileReference: cover.FileReference, ThumbSize: kind}
		}); image != "" {
			return image
		}
	}
	return fetchTelegramThumbnail(ctx, client, video.doc.DCID, telegramThumbs(video.doc.Thumbs), func(kind string) tg.InputFileLocationClass {
		return documentLocation(video.doc, kind)
	})
}

func fetchTelegramThumbnail(
	ctx context.Context, client *telegram.Client, dc int, candidates []telegramThumb,
	location func(string) tg.InputFileLocationClass,
) string {
	var invoker tg.Invoker
	for _, candidate := range candidates {
		if candidate.inline != nil {
			if image := telegramThumbnailURL(candidate.inline); image != "" {
				return image
			}
			continue
		}
		if candidate.kind == "" || ctx.Err() != nil {
			continue
		}
		if invoker == nil {
			conn, err := client.FileDC(ctx, dc)
			if err != nil {
				continue
			}
			invoker = conn
		}
		buffer := new(telegramThumbnailWriter)
		if _, err := client.DownloadVia(invoker, location(candidate.kind)).WithSize(int64(candidate.bytes)).Stream(ctx, buffer); err != nil {
			continue
		}
		if image := telegramThumbnailURL(buffer.Bytes()); image != "" {
			return image
		}
	}
	return ""
}

type telegramThumbnailWriter struct{ bytes.Buffer }

func (b *telegramThumbnailWriter) Write(data []byte) (int, error) {
	if len(data) > maxTelegramThumbnail-b.Len() {
		return 0, errors.New("thumbnail exceeds its size limit")
	}
	return b.Buffer.Write(data)
}

func telegramThumbnailURL(data []byte) string {
	if len(data) == 0 || len(data) > maxTelegramThumbnail {
		return ""
	}
	switch mime := http.DetectContentType(data); mime {
	case "image/jpeg", "image/png", "image/webp":
		return "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(data)
	default:
		return ""
	}
}
