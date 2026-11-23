// SPDX-License-Identifier: MIT
package main

import (
	"context"

	"github.com/TNT-Likely/telegram-nestfetch/engine/preview"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
)

func (p *telegramProvider) Preview(parent context.Context, link MessageLink, ready func(string)) error {
	client, ctx, release, err := p.sessionFor(parent)
	if err != nil {
		return err
	}
	defer release()

	lookup, cancel := context.WithTimeout(ctx, connectDeadline)
	attachment, err := findTelegramMedia(lookup, client.API(), link)
	cancel()
	if err != nil {
		return telegramOperationError(ctx, err)
	}
	if attachment.media.Kind != MediaVideo {
		return telegramFailure("preview_video_required")
	}
	if attachment.media.HideThumbnail {
		return telegramFailure("protected_media")
	}

	contentType := preview.ContentType(attachment.media.MIME, attachment.media.Name)
	location, dc := attachment.location, attachment.dc
	read := func(ctx context.Context, offset int64, block []byte) (int, error) {
		invoker, err := client.FileDC(ctx, dc)
		if err != nil {
			return 0, err
		}
		api := tg.NewClient(invoker)
		n, err := readTelegramPreviewBlock(ctx, api, location, offset, block)
		if !downloader.IsFileReferenceError(err) {
			return n, err
		}

		updated, err := fetchTelegramMedia(ctx, client.API(), attachment.channel, link, attachment.media.Source)
		if err != nil {
			return 0, err
		}
		if !sameTelegramAttachment(attachment.media, updated.media) {
			return 0, telegramFailure("media_changed")
		}
		location, dc = updated.location, updated.dc
		invoker, err = client.FileDC(ctx, dc)
		if err != nil {
			return 0, err
		}
		return readTelegramPreviewBlock(ctx, tg.NewClient(invoker), location, offset, block)
	}

	err = preview.Serve(ctx, attachment.media.Bytes, contentType, read, func(address string) error {
		ready(address)
		return nil
	})
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return telegramFailure("preview_unavailable")
	}
	return nil
}

func readTelegramPreviewBlock(ctx context.Context, api *tg.Client, location tg.InputFileLocationClass, offset int64, block []byte) (int, error) {
	req := &tg.UploadGetFileRequest{Location: location, Offset: offset, Limit: len(block)}
	req.SetPrecise(true)
	reply, err := api.UploadGetFile(ctx, req)
	if err != nil {
		return 0, err
	}
	if file, ok := reply.(*tg.UploadFile); ok && len(file.Bytes) <= len(block) {
		return copy(block, file.Bytes), nil
	}
	return 0, telegramFailure("preview_unavailable")
}
