// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (p *tdlProvider) ListChannels(ctx context.Context) ([]channelSummary, error) {
	channels := make([]channelSummary, 0)
	if err := p.requireTDLSession(); err != nil {
		return nil, err
	}
	data, err := p.catalogExport(ctx, 4*1024*1024, "chat", "ls", "--output", "json", "--channels-only", "--output-file")
	if err != nil {
		return nil, err
	}
	var entries []struct {
		ID       int64  `json:"id"`
		Type     string `json:"type"`
		Name     string `json:"visible_name"`
		Username string `json:"username"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, telegramFailure("telegram_error")
	}
	seen := make(map[int64]bool, len(entries))
	for _, entry := range entries {
		if entry.Type != "channel" || entry.ID <= 0 || seen[entry.ID] {
			continue
		}
		seen[entry.ID] = true
		id := strconv.FormatInt(entry.ID, 10)
		title := telegramDisplayText(entry.Name)
		if title == "" {
			title = id
		}
		channels = append(channels, channelSummary{ID: id, Title: title, Username: channelUsername(entry.Username)})
	}
	sortChannels(channels)
	return channels, nil
}

func (p *tdlProvider) ListAttachments(ctx context.Context, query channelQuery) (channelPage, error) {
	if err := p.requireTDLSession(); err != nil {
		return channelPage{}, err
	}
	args := []string{
		"chat", "export", "--chat", query.Channel, "--type", "time",
		"--input", fmt.Sprintf("%d,%d", query.From, query.To),
		"--page-size", strconv.Itoa(channelPageSize), "--before-id", strconv.Itoa(query.Before),
		"--raw", "--respect-restrictions", "--output",
	}
	data, err := p.catalogExport(ctx, 16*1024*1024, args...)
	if transferFailure(err).Code == "access_denied" && !strings.HasPrefix(query.Channel, "@") {
		// Refresh the peer cache for a private channel pasted from Telegram.
		if _, refreshErr := p.ListChannels(ctx); refreshErr != nil {
			return channelPage{}, refreshErr
		}
		data, err = p.catalogExport(ctx, 16*1024*1024, args...)
	}
	if err != nil {
		return channelPage{}, err
	}
	return decodeTDLChannelPage(data, query)
}

func (p *tdlProvider) catalogExport(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	dir, err := os.MkdirTemp(p.dir, "catalog-")
	if err != nil {
		return nil, telegramFailure("disk_write_failed")
	}
	defer os.RemoveAll(dir)
	output := filepath.Join(dir, "result.json")
	if err := p.runTDLExport(ctx, append(args, output)); err != nil {
		return nil, err
	}
	data, err := boundedRead(output, limit)
	if err != nil {
		return nil, telegramFailure("telegram_error")
	}
	return data, nil
}

func decodeTDLChannelPage(data []byte, query channelQuery) (channelPage, error) {
	var archive struct {
		tdlArchive
		NextBefore int
		Title      string
		Username   string
	}
	if err := json.Unmarshal(data, &archive); err != nil || archive.ID <= 0 || len(archive.Messages) > channelPageSize {
		return channelPage{}, telegramFailure("telegram_error")
	}
	if expected, numeric := positiveID(query.Channel, 64); numeric && archive.ID != expected {
		return channelPage{}, telegramFailure("access_denied")
	}
	if archive.NextBefore < 0 || archive.NextBefore > math.MaxInt32 ||
		(query.Before != 0 && archive.NextBefore >= query.Before) {
		return channelPage{}, telegramFailure("telegram_error")
	}
	username := archive.Username
	if linkName, ok := strings.CutPrefix(query.Channel, "@"); ok {
		username = linkName
	}
	page := channelPage{Items: make([]channelAttachment, 0, len(archive.Messages)), NextBefore: archive.NextBefore}
	source := telegramDisplayText(archive.Title)
	seen := make(map[int]bool, len(archive.Messages))
	for _, item := range archive.Messages {
		if item.ID <= 0 || item.ID > math.MaxInt32 || seen[item.ID] || item.Raw == nil ||
			(query.Before != 0 && item.ID >= query.Before) ||
			item.Raw.Date < query.From || item.Raw.Date > query.To {
			continue
		}
		record, err := decodeTDLMessage(item, archive.ID, source)
		if err != nil {
			continue
		}
		seen[item.ID] = true
		page.Items = append(page.Items, browsedAttachment(channelMessageURL(archive.ID, username, item.ID), record.media, item.Raw.Date))
	}
	return page, nil
}
