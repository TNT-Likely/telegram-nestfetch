// SPDX-License-Identifier: MIT
package main

import (
	"math"
	"mime"
	"path"
	"strconv"
	"strings"
)

func describeDocument(media Media, messageID int) Media {
	media.MIME, _, _ = strings.Cut(strings.ToLower(media.MIME), ";")
	media.MIME = strings.TrimSpace(media.MIME)
	media.Name = telegramDisplayText(path.Base(strings.ReplaceAll(media.Name, "\\", "/")))
	ext := path.Ext(media.Name)
	if media.MIME == "" || media.MIME == "application/octet-stream" {
		if guessed := mime.TypeByExtension(strings.ToLower(ext)); attachmentKind(guessed) != MediaFile {
			media.MIME = guessed
		}
	}
	if media.Kind == "" {
		media.Kind = attachmentKind(media.MIME)
	}
	if media.Kind == MediaVideo {
		media.Name = telegramVideoFilename(media.Name, media.MIME, messageID)
	} else {
		if strings.Trim(media.Name, ". ") == "" {
			media.Name = "attachment_" + strconv.Itoa(messageID)
			ext = ""
		}
		if ext == "" {
			if extensions, _ := mime.ExtensionsByType(media.MIME); len(extensions) > 0 {
				media.Name += extensions[0]
			}
		}
	}
	media.Name = filename(media.Name)
	media.Title = media.Name
	if media.Kind == MediaVideo {
		media.Title = strings.TrimSuffix(media.Name, path.Ext(media.Name))
	}
	if math.IsNaN(media.Duration) || math.IsInf(media.Duration, 0) || media.Duration < 0 {
		media.Duration = 0
	}
	return media
}

func attachmentKind(mimeType string) MediaKind {
	switch {
	case strings.HasPrefix(mimeType, "video/"):
		return MediaVideo
	case strings.HasPrefix(mimeType, "image/"):
		return MediaImage
	case strings.HasPrefix(mimeType, "audio/") || mimeType == "application/ogg":
		return MediaAudio
	default:
		return MediaFile
	}
}

func photoMedia(id, bytes int64, source string, spoiler bool) Media {
	base := strconv.FormatInt(id, 10)
	return Media{
		ID: id, Kind: MediaImage, MIME: "image/jpeg", Name: base + ".jpg", Title: base,
		Source: source, Bytes: bytes, HideThumbnail: spoiler,
	}
}

func sameTelegramAttachment(a, b Media) bool {
	return a.ID == b.ID && a.Bytes == b.Bytes && a.Kind == b.Kind && a.HideThumbnail == b.HideThumbnail
}
