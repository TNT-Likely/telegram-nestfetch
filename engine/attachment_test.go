// SPDX-License-Identifier: MIT
package main

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestTelegramAttachmentMetadataAcrossBackends(t *testing.T) {
	channel := &tg.InputChannel{ChannelID: tdlFixtureChannel}
	photo := &tg.Photo{
		ID: 200, DCID: 4, Sizes: []tg.PhotoSizeClass{
			&tg.PhotoSize{Type: "m", Size: 20},
			&tg.PhotoSizeProgressive{Type: "x", Sizes: []int{40, 120}},
			&tg.PhotoStrippedSize{Type: "i"},
		},
	}
	document := &tg.Document{
		ID: 201, DCID: 2, Size: 145, MimeType: "application/pdf",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "资料 01.pdf"}},
	}
	audio := &tg.Document{
		ID: 202, DCID: 2, Size: 167, MimeType: "audio/ogg",
		Attributes: []tg.DocumentAttributeClass{
			&tg.DocumentAttributeAudio{Duration: 12, Voice: true},
			&tg.DocumentAttributeFilename{FileName: "voice.ogg"},
		},
	}
	image := &tg.Document{
		ID: 203, DCID: 2, Size: 85, MimeType: "image/webp",
		Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: "image.webp"}},
	}
	cases := []struct {
		name     string
		raw      tg.MessageMediaClass
		kind     MediaKind
		size     int64
		duration float64
	}{
		// The live-photo motion document is not the selected image.
		{"200.jpg", &tg.MessageMediaPhoto{Photo: photo, LivePhoto: true, Video: document}, MediaImage, 120, 0},
		{"资料 01.pdf", &tg.MessageMediaDocument{Document: document}, MediaFile, 145, 0},
		{"voice.ogg", &tg.MessageMediaDocument{Document: audio, Voice: true}, MediaAudio, 167, 12},
		{"image.webp", &tg.MessageMediaDocument{Document: image}, MediaImage, 85, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			msg := &tg.Message{
				ID: tdlFixtureMessage, PeerID: &tg.PeerChannel{ChannelID: tdlFixtureChannel}, Media: c.raw,
			}
			native, err := telegramMediaFromMessage(msg, channel, "")
			if err != nil {
				t.Fatal(err)
			}
			link := MessageLink{ChannelID: tdlFixtureChannel, MessageID: tdlFixtureMessage}
			tdl, err := decodeTDLArchive(encodeTestTDL(t, channel.ChannelID, msg.ID, c.name, msg), link)
			if err != nil {
				t.Fatal(err)
			}
			if native.media != tdl.media || native.media.Name != c.name || native.media.Kind != c.kind ||
				native.media.Bytes != c.size || native.media.Duration != c.duration {
				t.Fatalf("attachment metadata mismatch, native %+v, tdl %+v", native.media, tdl.media)
			}
			if c.name == "200.jpg" {
				location, ok := native.location.(*tg.InputPhotoFileLocation)
				if !ok || location.ID != photo.ID || location.ThumbSize != "x" || native.dc != photo.DCID {
					t.Fatalf("unexpected photo location: %T %+v", native.location, native.location)
				}
			}
		})
	}
}

func TestTimedPhotosRemainProtected(t *testing.T) {
	channel := &tg.InputChannel{ChannelID: tdlFixtureChannel}
	photo := &tg.MessageMediaPhoto{Photo: &tg.Photo{
		ID: 200, Sizes: []tg.PhotoSizeClass{&tg.PhotoSize{Type: "x", Size: 120}},
	}}
	// Presence of ttl_seconds is restricted, including an explicit zero.
	photo.SetTTLSeconds(0)
	msg := &tg.Message{
		ID: tdlFixtureMessage, PeerID: &tg.PeerChannel{ChannelID: tdlFixtureChannel}, Media: photo,
	}
	_, err := telegramMediaFromMessage(msg, channel, "")
	expectCode(t, err, "protected_media")
	link := MessageLink{ChannelID: tdlFixtureChannel, MessageID: tdlFixtureMessage}
	_, err = decodeTDLArchive(encodeTestTDL(t, channel.ChannelID, msg.ID, "200.jpg", msg), link)
	expectCode(t, err, "protected_media")
}
