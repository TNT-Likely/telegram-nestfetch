// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
)

const tdlFixtureChannel = int64(893455788905)
const tdlFixtureDocument = int64(9045020786317749001)
const tdlFixtureMessage = 167940
const tdlFixtureBytes = "synthetic file test bytes\n"

func encodeTestTDL(t *testing.T, channel int64, id int, file string, raw *tg.Message) []byte {
	t.Helper()
	wire := map[string]any{
		"id": channel,
		"messages": []any{map[string]any{
			"id": id, "type": "message", "file": file, "raw": raw,
		}},
	}
	payload, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func tdlVideoMessage() *tg.Message {
	return &tg.Message{
		ID: tdlFixtureMessage, PeerID: &tg.PeerChannel{ChannelID: tdlFixtureChannel},
		Media: &tg.MessageMediaDocument{Document: &tg.Document{
			ID: tdlFixtureDocument, Size: int64(len(tdlFixtureBytes)), MimeType: "application/octet-stream",
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeVideo{Duration: 5, W: 120, H: 72},
				&tg.DocumentAttributeFilename{FileName: "Clip #1.mp4"},
			},
		}},
	}
}

func TestTDLRawArchivePinsExactMessageAndVideo(t *testing.T) {
	link := MessageLink{ChannelID: tdlFixtureChannel, MessageID: tdlFixtureMessage}
	raw := tdlVideoMessage()
	archive := encodeTestTDL(t, tdlFixtureChannel, tdlFixtureMessage, "Clip #1.mp4", raw)
	record, err := decodeTDLArchive(archive, link)
	if err != nil {
		t.Fatal(err)
	}
	if record.peerID != tdlFixtureChannel || record.messageID != tdlFixtureMessage ||
		record.media.ID != tdlFixtureDocument || record.media.Name != "Clip #1.mp4" ||
		record.media.Duration != 5 || record.media.Bytes != int64(len(tdlFixtureBytes)) {
		t.Fatalf("invalid parsed metadata: %+v", record)
	}

	link.MessageID++
	_, err = decodeTDLArchive(archive, link)
	expectCode(t, err, "message_not_found")
	link.MessageID--
	link.ChannelID++
	_, err = decodeTDLArchive(archive, link)
	expectCode(t, err, "message_not_found")
	link.ChannelID--

	attachment := raw.Media.(*tg.MessageMediaDocument)
	attachment.Document.(*tg.Document).Attributes = []tg.DocumentAttributeClass{
		&tg.DocumentAttributeAudio{Duration: 5},
		&tg.DocumentAttributeImageSize{W: 120, H: 72},
	}
	archive = encodeTestTDL(t, tdlFixtureChannel, tdlFixtureMessage, "audio.mp3", raw)
	audio, err := decodeTDLArchive(archive, link)
	if err != nil || audio.media.Kind != MediaAudio || audio.media.Name != "audio.mp3" || audio.media.Duration != 5 {
		t.Fatalf("audio metadata mismatch: %+v %v", audio.media, err)
	}
}

func TestTDLSavedArchivePinsBothOwnerAndPeerType(t *testing.T) {
	link := MessageLink{SavedAccountID: tdlFixtureChannel, MessageID: tdlFixtureMessage}
	raw := tdlVideoMessage()
	archive := func(owner int64) []byte {
		return encodeTestTDL(t, owner, tdlFixtureMessage, "Clip #1.mp4", raw)
	}
	_, err := decodeTDLArchive(archive(tdlFixtureChannel), link)
	expectCode(t, err, "message_not_found")

	raw.PeerID = &tg.PeerUser{UserID: tdlFixtureChannel}
	record, err := decodeTDLArchive(archive(tdlFixtureChannel), link)
	if err != nil || !record.saved || record.peerID != tdlFixtureChannel || record.media.ID != tdlFixtureDocument {
		t.Fatalf("expected authenticated Saved Messages attachment: %+v, %v", record, err)
	}
	_, err = decodeTDLArchive(archive(tdlFixtureChannel+1), link)
	expectCode(t, err, "saved_account_mismatch")
}

func TestTDLCoverAndProtection(t *testing.T) {
	dot := image.NewRGBA(image.Rect(0, 0, 2, 2))
	dot.Set(0, 0, color.RGBA{R: 255, A: 255})
	var imagePNG bytes.Buffer
	if err := png.Encode(&imagePNG, dot); err != nil {
		t.Fatal(err)
	}

	link := MessageLink{ChannelID: tdlFixtureChannel, MessageID: tdlFixtureMessage}
	raw := tdlVideoMessage()
	attachment := raw.Media.(*tg.MessageMediaDocument)
	attachment.VideoCover = &tg.Photo{Sizes: []tg.PhotoSizeClass{
		&tg.PhotoCachedSize{W: 2, H: 2, Bytes: imagePNG.Bytes()},
	}}
	parse := func() (tdlMediaRecord, error) {
		testArchive := encodeTestTDL(t, tdlFixtureChannel, tdlFixtureMessage, "Clip #1.mp4", raw)
		return decodeTDLArchive(testArchive, link)
	}
	covered, err := parse()
	if err != nil || !strings.HasPrefix(covered.media.Thumbnail, "data:image/png;base64,") {
		t.Fatalf("inline official cover missing: %+v %v", covered, err)
	}

	attachment.Spoiler = true
	spoiler, err := parse()
	if err != nil || !spoiler.media.HideThumbnail || spoiler.media.Thumbnail != "" {
		t.Fatalf("spoiler exposed: %+v %v", spoiler, err)
	}

	attachment.Flags.Set(2)
	_, err = parse()
	expectCode(t, err, "protected_media")
	attachment.Flags.Unset(2)
	raw.Noforwards = true
	_, err = parse()
	expectCode(t, err, "protected_media")
}

func mockTDLMediaProvider(t *testing.T, scenario string) *tdlProvider {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("subprocess fixture uses a POSIX shell")
	}
	root := t.TempDir()
	path := filepath.Join(root, "fixture-tdl")
	content := `#!/bin/sh
while test "$#" -gt 0; do
  case "$1" in
    chat|download) break ;;
    *) shift ;;
  esac
done
action="$1"
shift
case "$action" in
  chat)
    mode="$1"
    shift
    if test "$mode" = "ls"; then
      printf '[]\n'
      touch private-peer-seen
      exit 0
    fi
    out=
    topic=
    while test "$#" -gt 0; do
      case "$1" in
        --output) out="$2"; shift ;;
        --topic) topic="$2"; shift ;;
        --input) test "$2" = "167940,167940" || exit 9; shift ;;
      esac
      shift
    done
    test -n "$out" || exit 8
    if test "$SCENARIO" = "private-refresh" && test ! -f private-peer-seen; then
      printf 'failed to get peer: failed to get result from 893455788905\n' >&2
      exit 7
    fi
    if test "$SCENARIO" = "topic-wrong-peer"; then
      if test -n "$topic"; then
        cp wrong-peer.json "$out"
      else
        printf '{"id":893455788905,"messages":[]}\n' > "$out"
      fi
    elif test "$SCENARIO" = "edit" && test -f downloaded; then
      cp edited.json "$out"
    else
      cp fixture.json "$out"
    fi ;;
  download)
    to=
    url=
    template=
    while test "$#" -gt 0; do
      case "$1" in
        --dir) to="$2"; shift ;;
        --url) url="$2"; shift ;;
        --template) template="$2"; shift ;;
        --serve|--group|--skip-same) exit 12 ;;
      esac
      shift
    done
    test "$url" = 'https://t.me/c/893455788905/167940' || exit 6
    case "$template" in
      *'eq .DialogID 893455788905'*'eq .MessageID 167940'*'eq .FileName "Clip #1.mp4"'*) ;;
      *) exit 5 ;;
    esac
    case "$SCENARIO" in
      incomplete) printf 'only prefix' > "$to/nestfetch-file" ;;
      skipped) printf 'No source remained\n' ;;
      extra)
        cp fixture.mp4 "$to/nestfetch-file"
        cp fixture.mp4 "$to/another-file" ;;
      cancel)
        trap 'exit 1' INT TERM
        printf 'only prefix' > "$to/nestfetch-file.tmp"
        while true; do sleep 0.1; done ;;
      *)
        cp fixture.mp4 "$to/nestfetch-file"
        touch downloaded ;;
    esac ;;
  *) exit 4 ;;
esac
`
	if err := os.WriteFile(path, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCENARIO", scenario)
	provider, err := newTDLProvider(root, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	provider.account = tdlAccount("connected")

	raw := tdlVideoMessage()
	wrong := tdlVideoMessage()
	wrong.PeerID = &tg.PeerChannel{ChannelID: tdlFixtureChannel + 1}
	edited := tdlVideoMessage()
	edited.Media.(*tg.MessageMediaDocument).Document.(*tg.Document).ID++
	for name, data := range map[string][]byte{
		"fixture.json":    encodeTestTDL(t, tdlFixtureChannel, tdlFixtureMessage, "Clip #1.mp4", raw),
		"wrong-peer.json": encodeTestTDL(t, tdlFixtureChannel+1, tdlFixtureMessage, "Clip #1.mp4", wrong),
		"edited.json":     encodeTestTDL(t, tdlFixtureChannel, tdlFixtureMessage, "Clip #1.mp4", edited),
		"fixture.mp4":     []byte(tdlFixtureBytes),
	} {
		if err := os.WriteFile(filepath.Join(provider.dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return provider
}

func tdlFixtureLink(t *testing.T, private bool) MessageLink {
	t.Helper()
	s := "https://t.me/sample/"
	if private {
		s = "https://t.me/c/" + strconv.FormatInt(tdlFixtureChannel, 10) + "/"
	}
	link, err := parseMessageLink(s + strconv.Itoa(tdlFixtureMessage) + "?single")
	if err != nil {
		t.Fatal(err)
	}
	return link
}

func TestTDLSubprocessOnlyAdoptsVerifiedCompleteVideo(t *testing.T) {
	cases := []struct{ mode, errorCode string }{
		{"valid", ""}, {"private-refresh", ""}, {"incomplete", "download_incomplete"},
		{"skipped", "download_incomplete"}, {"extra", "download_incomplete"}, {"edit", "media_changed"},
	}
	for _, tc := range cases {
		t.Run(tc.mode, func(t *testing.T) {
			p := mockTDLMediaProvider(t, tc.mode)
			link := tdlFixtureLink(t, tc.mode == "private-refresh")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			media, err := p.Inspect(ctx, link)
			if err != nil {
				t.Fatal(err)
			}
			// A stale same-size staging file must not count as a new download.
			target := filepath.Join(t.TempDir(), "download.part")
			original := strings.Repeat("a", len(tdlFixtureBytes))
			if err := os.WriteFile(target, []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			stale, err := os.MkdirTemp(filepath.Dir(target), ".tdl-")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(stale, tdlTransferName), []byte(original), 0600); err != nil {
				t.Fatal(err)
			}
			latest := int64(-1)
			err = p.Download(ctx, link, media, target, func(n int64) { latest = n })
			if tc.errorCode != "" {
				expectCode(t, err, tc.errorCode)
				if readFile(t, target) != original || latest == media.Bytes {
					t.Fatal("unverified bytes were adopted")
				}
			} else if err != nil || readFile(t, target) != tdlFixtureBytes || latest != media.Bytes {
				t.Fatalf("download failed: %v, progress=%d", err, latest)
			}
			files, err := os.ReadDir(filepath.Dir(target))
			if err != nil || len(files) != 1 || files[0].Name() != "download.part" {
				t.Fatalf("temporary download was retained: %v %v", files, err)
			}
		})
	}
}

func TestTDLTopicCannotSwitchToLinkedDiscussion(t *testing.T) {
	p := mockTDLMediaProvider(t, "topic-wrong-peer")
	link := tdlFixtureLink(t, false)
	link.TopicID = 17
	_, err := p.Inspect(context.Background(), link)
	expectCode(t, err, "message_not_found")
}

func TestTDLCancelDiscardsUnfinishedCLIBytes(t *testing.T) {
	p := mockTDLMediaProvider(t, "cancel")
	link := tdlFixtureLink(t, false)
	media, err := p.Inspect(context.Background(), link)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	target := filepath.Join(t.TempDir(), "download.part")
	reported := int64(0)
	err = p.Download(ctx, link, media, target, func(n int64) {
		reported = n
		if reported > 0 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || reported <= 0 || reported >= media.Bytes {
		t.Fatalf("expected a canceled in-flight transfer, received %v at %d", err, reported)
	}
	if files, err := os.ReadDir(filepath.Dir(target)); err != nil || len(files) != 0 {
		t.Fatalf("unfinished output retained: %v %v", files, err)
	}
}

func TestTDLCleanupDoesNotFollowSymlinkStaging(t *testing.T) {
	external := t.TempDir()
	foreign := filepath.Join(external, "job", ".tdl-foreign")
	if err := os.MkdirAll(foreign, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(foreign, "personal.dat")
	if err := os.WriteFile(file, []byte("keep this"), 0600); err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	stage := filepath.Join(parent, "staging")
	symlink := func(source, target string) {
		t.Helper()
		if err := os.Symlink(source, target); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symbolic link unavailable: %v", err)
			}
			t.Fatal(err)
		}
	}
	symlink(external, stage)
	link := filepath.Join(parent, "job")
	symlink(filepath.Dir(foreign), link)
	for _, job := range []string{filepath.Join(stage, "job"), link} {
		if err := discardTDLTransferDirs(job); err == nil {
			t.Fatalf("refused to reject symbolic link %q", job)
		}
		if readFile(t, file) != "keep this" {
			t.Fatal("external data changed")
		}
	}
}
