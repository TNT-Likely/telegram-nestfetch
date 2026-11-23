// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"strconv"
	"strings"

	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/tg"
)

func (p *telegramProvider) ListChannels(parent context.Context) ([]channelSummary, error) {
	client, ctx, cleanup, err := p.sessionFor(parent)
	if err != nil {
		return nil, err
	}
	defer cleanup()

	out := make([]channelSummary, 0)
	seen := make(map[int64]bool)
	builder := dialogs.NewQueryBuilder(client.API()).GetDialogs()
	for _, folder := range []int{0, 1} {
		iter := builder.FolderID(folder).Iter()
		for iter.Next(ctx) {
			entry := iter.Value()
			peer, ok := entry.Peer.(*tg.InputPeerChannel)
			if !ok || seen[peer.ChannelID] {
				continue
			}
			channel := entry.Entities.Channels()[peer.ChannelID]
			if channel == nil || !channel.Broadcast || channel.Left {
				continue
			}
			seen[channel.ID] = true
			id := strconv.FormatInt(channel.ID, 10)
			title := telegramDisplayText(channel.Title)
			if title == "" {
				title = id
			}
			out = append(out, channelSummary{ID: id, Title: title, Username: channelUsername(channel.Username)})
		}
		if err := iter.Err(); err != nil {
			return nil, telegramOperationError(ctx, err)
		}
	}
	sortChannels(out)
	return out, nil
}

func (p *telegramProvider) ListAttachments(parent context.Context, filter channelQuery) (channelPage, error) {
	client, ctx, cleanup, err := p.sessionFor(parent)
	if err != nil {
		return channelPage{}, err
	}
	defer cleanup()

	var link MessageLink
	if username, ok := strings.CutPrefix(filter.Channel, "@"); ok {
		link.Username = username
	} else {
		link.ChannelID, _ = positiveID(filter.Channel, 64)
	}
	channel, source, err := resolveTelegramChannel(ctx, client.API(), link)
	if err != nil {
		return channelPage{}, telegramOperationError(ctx, err)
	}
	history := query.NewQuery(client.API()).Messages().GetHistory(&tg.InputPeerChannel{
		ChannelID: channel.ChannelID, AccessHash: channel.AccessHash,
	})
	iter := history.BatchSize(100).Iter()
	if filter.Before != 0 {
		iter.OffsetID(filter.Before)
	} else {
		iter.OffsetDate(filter.To + 1)
	}

	page := channelPage{Items: make([]channelAttachment, 0, channelPageSize)}
	scanned, lastID := 0, 0
	for iter.Next(ctx) {
		item := iter.Value()
		if c := item.Entities.Channels()[channel.ChannelID]; c != nil {
			if c.Noforwards {
				return channelPage{}, telegramFailure("protected_media")
			}
			if link.Username == "" {
				link.Username = c.Username
			}
		}
		if item.Msg.GetDate() < filter.From {
			break
		}
		id := item.Msg.GetID()
		if filter.Before != 0 && id >= filter.Before {
			continue
		}
		if len(page.Items) >= channelPageSize || scanned >= channelScanLimit {
			page.NextBefore = lastID
			break
		}
		scanned++
		lastID = id
		msg, ok := item.Msg.(*tg.Message)
		if !ok || msg.Date > filter.To {
			continue
		}
		peer, ok := msg.PeerID.(*tg.PeerChannel)
		if !ok || peer.ChannelID != channel.ChannelID {
			continue
		}
		attachment, err := telegramMediaFromMessage(msg, channel, source)
		if err != nil {
			continue
		}
		attachment.media.Thumbnail = inlineTelegramCover(attachment)
		page.Items = append(page.Items, browsedAttachment(channelMessageURL(channel.ChannelID, link.Username, id), attachment.media, msg.Date))
	}
	if err := iter.Err(); err != nil {
		return channelPage{}, telegramOperationError(ctx, err)
	}
	return page, ctx.Err()
}

func inlineTelegramCover(attachment telegramMedia) string {
	if attachment.media.HideThumbnail {
		return ""
	}
	var sizes []tg.PhotoSizeClass
	if attachment.cover != nil {
		sizes = attachment.cover.Sizes
	} else if attachment.doc != nil {
		sizes = attachment.doc.Thumbs
	}
	for _, candidate := range telegramThumbs(sizes) {
		if len(candidate.inline) <= 54*1024 {
			if preview := telegramThumbnailURL(candidate.inline); preview != "" {
				return preview
			}
		}
	}
	return ""
}
