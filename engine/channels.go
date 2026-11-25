// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"math"
	"net/url"
	"slices"
	"strconv"
	"strings"
)

const channelPageSize = 48
const channelScanLimit = 500

type channelSummary struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Username string `json:"username,omitempty"`
}

type channelQuery struct {
	Channel string `json:"channel"`
	Saved   bool   `json:"saved,omitempty"`
	From    int    `json:"from"`
	To      int    `json:"to"`
	Before  int    `json:"before"`
}

type channelAttachment struct {
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Name      string    `json:"name"`
	Kind      MediaKind `json:"kind"`
	Size      int64     `json:"size"`
	Date      int       `json:"date"`
	Duration  float64   `json:"duration"`
	Thumbnail string    `json:"thumbnail,omitempty"`
}

type channelPage struct {
	Items      []channelAttachment
	NextBefore int
}

type channelBrowser interface {
	ListChannels(context.Context) ([]channelSummary, error)
	ListAttachments(context.Context, channelQuery) (channelPage, error)
}

func (q *channelQuery) validate() error {
	if q.Saved {
		if q.Channel != "" {
			return &Failure{Code: "invalid_channel"}
		}
	} else {
		selector, err := parseChannelSelector(q.Channel)
		if err != nil {
			return err
		}
		q.Channel = selector
	}
	// Telegram dates and message IDs are signed 32-bit integers. The CLI uses
	// the second after the inclusive end of the window as the initial offset.
	if q.From <= 0 || q.To < q.From || q.To >= math.MaxInt32 || q.Before < 0 || q.Before > math.MaxInt32 {
		return &Failure{Code: "invalid_date_range"}
	}
	return nil
}

func parseChannelSelector(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	invalid := &Failure{Code: "invalid_channel"}
	if raw == "" || len(raw) > 2048 {
		return "", invalid
	}
	if id, ok := positiveID(strings.TrimPrefix(raw, "-100"), 64); ok {
		return strconv.FormatInt(id, 10), nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "t.me/") {
		raw = "https://" + raw
	}
	if link, err := parseMessageLink(raw); err == nil && link.SavedAccountID == 0 {
		if link.ChannelID != 0 {
			return strconv.FormatInt(link.ChannelID, 10), nil
		}
		return "@" + link.Username, nil
	}
	if strings.Contains(raw, "://") {
		address, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(address.Scheme, "https") || !strings.EqualFold(address.Host, "t.me") ||
			address.User != nil || address.RawPath != "" || address.RawQuery != "" || address.Fragment != "" || address.Opaque != "" {
			return "", invalid
		}
		raw = strings.Trim(strings.TrimPrefix(address.Path, "/s/"), "/")
		if id, ok := positiveID(strings.TrimPrefix(raw, "c/"), 64); ok && strings.HasPrefix(raw, "c/") {
			return strconv.FormatInt(id, 10), nil
		}
	}
	if name := channelUsername(strings.TrimPrefix(raw, "@")); name != "" {
		return "@" + name, nil
	}
	return "", invalid
}

func channelUsername(name string) string {
	name = strings.ToLower(name)
	if !usernamePattern.MatchString(name) || reservedUsername(name) || digitsPattern.MatchString(name) {
		return ""
	}
	return name
}

func sortChannels(channels []channelSummary) {
	slices.SortStableFunc(channels, func(a, b channelSummary) int {
		return strings.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
	})
}

func channelMessageURL(channel int64, username string, message int) string {
	peer := channelUsername(username)
	if peer == "" {
		peer = "c/" + strconv.FormatInt(channel, 10)
	}
	return "https://t.me/" + peer + "/" + strconv.Itoa(message)
}

func browsedAttachment(link string, media Media, date int) channelAttachment {
	// A page has a limited payload. Full-size Telegram covers are still
	// cached when this message becomes a queue job.
	preview := media.Thumbnail
	if len(preview) > 72*1024 || media.HideThumbnail {
		preview = ""
	}
	return channelAttachment{
		URL: link, Title: media.Title, Name: filename(media.Name), Kind: media.Kind,
		Size: media.Bytes, Date: date, Duration: media.Duration, Thumbnail: preview,
	}
}
