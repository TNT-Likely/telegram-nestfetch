// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/telegram/thumbnail"
)

const (
	tdlTransferName = "nestfetch-video"
	tdlMetadataMax  = 4 * 1024 * 1024
	tdlTransferIdle = 90 * time.Second
)

// The pinned tdl release serializes its Go MTProto structs in the raw
// attachment. Decode only fields needed for one video and its inline preview.
type tdlArchive struct {
	ID       int64
	Messages []tdlArchiveMessage
}

type tdlArchiveMessage struct {
	ID   int
	Type string
	File string
	Raw  *tdlRawMessage
}

type tdlRawMessage struct {
	ID         int
	Flags      uint32
	Noforwards bool
	TTLPeriod  int
	PeerID     struct{ ChannelID int64 }
	Media      *tdlRawMedia
}

type tdlRawMedia struct {
	Flags      uint32
	Spoiler    bool
	Video      bool
	Round      bool
	TTLSeconds int
	Document   *tdlRawDocument
	VideoCover *struct{ Sizes []tdlInlinePhoto }
}

type tdlRawDocument struct {
	ID         int64
	Size       int64
	MimeType   string
	Thumbs     []tdlInlinePhoto
	Attributes []struct {
		FileName string
		Duration *float64
		W        *int
		H        *int
	}
}

type tdlInlinePhoto struct {
	Type  string
	W     int
	H     int
	Bytes []byte
}

type tdlMediaRecord struct {
	channelID int64
	messageID int
	file      string
	media     Media
}

func decodeTDLArchive(data []byte, link MessageLink) (tdlMediaRecord, error) {
	var archive tdlArchive
	if err := json.Unmarshal(data, &archive); err != nil {
		return tdlMediaRecord{}, &Failure{Code: "telegram_error"}
	}

	if len(archive.Messages) == 0 {
		return tdlMediaRecord{}, &Failure{Code: "message_not_found"}
	}
	if len(archive.Messages) != 1 || archive.ID <= 0 ||
		(link.ChannelID != 0 && archive.ID != link.ChannelID) {
		return tdlMediaRecord{}, &Failure{Code: "message_not_found"}
	}
	item := archive.Messages[0]
	if item.ID != link.MessageID || item.Raw == nil || item.Raw.ID != link.MessageID ||
		item.Type != "message" || item.Raw.PeerID.ChannelID != archive.ID {
		return tdlMediaRecord{}, &Failure{Code: "message_not_found"}
	}
	raw := item.Raw
	if raw.Noforwards || raw.Flags&(1<<26) != 0 || raw.TTLPeriod != 0 {
		return tdlMediaRecord{}, &Failure{Code: "protected_media"}
	}
	media := raw.Media
	if media == nil {
		return tdlMediaRecord{}, &Failure{Code: "no_video"}
	}
	doc := media.Document
	if doc == nil || doc.ID == 0 || doc.Size <= 0 || item.File == "" || len(item.File) > 4096 {
		return tdlMediaRecord{}, &Failure{Code: "no_video"}
	}
	// messageMediaDocument.ttl_seconds is in bit 2, including an
	// explicitly present zero-valued field in the generated raw struct.
	if media.TTLSeconds != 0 || media.Flags&(1<<2) != 0 {
		return tdlMediaRecord{}, &Failure{Code: "protected_media"}
	}

	isVideo := media.Video || media.Round || strings.HasPrefix(strings.ToLower(doc.MimeType), "video/")
	name := item.File
	duration := 0.0
	for _, attr := range doc.Attributes {
		if attr.FileName != "" {
			name = attr.FileName
		}
		if attr.Duration != nil && attr.W != nil && attr.H != nil {
			isVideo = true
			if *attr.Duration > 0 {
				duration = *attr.Duration
			}
		}
	}
	if !isVideo {
		return tdlMediaRecord{}, &Failure{Code: "no_video"}
	}
	name = telegramVideoFilename(name, doc.MimeType, link.MessageID)
	source := "@" + link.Username
	if link.Username == "" {
		source = ""
	}
	result := Media{
		ID: doc.ID, Name: name, Title: strings.TrimSuffix(name, filepath.Ext(name)),
		Source: source, Bytes: doc.Size, Duration: duration, HideThumbnail: media.Spoiler,
	}
	if !result.HideThumbnail {
		if media.VideoCover != nil {
			result.Thumbnail = inlineTDLThumbnail(media.VideoCover.Sizes)
		}
		if result.Thumbnail == "" {
			result.Thumbnail = inlineTDLThumbnail(doc.Thumbs)
		}
	}
	return tdlMediaRecord{channelID: archive.ID, messageID: item.ID, file: item.File, media: result}, nil
}

func inlineTDLThumbnail(photos []tdlInlinePhoto) string {
	preview := ""
	bestArea := 0
	for _, photo := range photos {
		if len(photo.Bytes) == 0 || len(photo.Bytes) > maxThumbnailBytes {
			continue
		}
		area := 0
		content := photo.Bytes
		if photo.W > 0 && photo.W <= 1600 && photo.H > 0 && photo.H <= 1600 {
			area = photo.W * photo.H
		} else if photo.Type == "i" && len(content) >= 3 && len(content) <= 1024 {
			// Telegram encodes stripped inline JPEG thumbnails without a
			// full JPEG header. Expand the preview with the existing decoder.
			var err error
			content, err = thumbnail.Expand(content)
			if err != nil {
				continue
			}
			area = 1
		}
		if area > bestArea {
			if image := telegramThumbnailURL(content); image != "" {
				bestArea, preview = area, image
			}
		}
	}
	return preview
}

func (p *tdlProvider) Inspect(ctx context.Context, link MessageLink) (Media, error) {
	if err := p.requireTDLSession(); err != nil {
		return Media{}, err
	}
	record, err := p.readTDLMedia(ctx, link, true)
	if err != nil {
		return Media{}, err
	}
	return record.media, nil
}

func (p *tdlProvider) requireTDLSession() error {
	if p.Status().State != "connected" {
		return &Failure{Code: "login_required"}
	}
	return nil
}

func (p *tdlProvider) readTDLMedia(ctx context.Context, link MessageLink, refreshPeer bool) (tdlMediaRecord, error) {
	if ctx.Err() != nil {
		return tdlMediaRecord{}, ctx.Err()
	}
	directory, err := os.MkdirTemp(p.dir, "inspect-")
	if err != nil {
		return tdlMediaRecord{}, &Failure{Code: "disk_write_failed"}
	}
	defer os.RemoveAll(directory)
	output := filepath.Join(directory, "export.json")

	chat := "@" + link.Username
	if link.ChannelID != 0 {
		chat = strconv.FormatInt(link.ChannelID, 10)
	}
	id := strconv.Itoa(link.MessageID)
	args := []string{
		"chat", "export", "--chat", chat,
		"--type", "id", "--input", id + "," + id,
		"--all", "--raw", "--output", output,
	}
	if err := p.runTDLExport(ctx, args); err != nil {
		if !refreshPeer || link.ChannelID == 0 || transferFailure(err).Code != "access_denied" {
			return tdlMediaRecord{}, err
		}
		// Listing with a false output filter refreshes tdl's private peer
		// cache without returning or storing any of the user's dialog list.
		if err := p.runTDLExport(ctx, []string{"chat", "ls", "--output", "json", "--filter", "false"}); err != nil {
			return tdlMediaRecord{}, err
		}
		if err := p.runTDLExport(ctx, args); err != nil {
			return tdlMediaRecord{}, err
		}
	}
	data, err := boundedRead(output, tdlMetadataMax)
	if err != nil {
		return tdlMediaRecord{}, &Failure{Code: "telegram_error"}
	}
	record, err := decodeTDLArchive(data, link)
	if link.TopicID == 0 || transferFailure(err).Code != "message_not_found" {
		return record, err
	}

	// Follow the topic only if the initial export positively identified the
	// channel. On a broadcast channel --topic can fetch a linked discussion
	// group instead, which is not the peer from the supplied message link.
	var original struct{ ID int64 }
	if parseErr := json.Unmarshal(data, &original); parseErr != nil ||
		original.ID <= 0 || (link.ChannelID != 0 && original.ID != link.ChannelID) {
		return record, err
	}
	if err := p.runTDLExport(ctx, append(args, "--topic", strconv.Itoa(link.TopicID))); err != nil {
		return tdlMediaRecord{}, err
	}
	link.ChannelID = original.ID
	data, err = boundedRead(output, tdlMetadataMax)
	if err != nil {
		return tdlMediaRecord{}, &Failure{Code: "telegram_error"}
	}
	return decodeTDLArchive(data, link)
}

func (p *tdlProvider) runTDLExport(ctx context.Context, args []string) error {
	cmd := p.tdlCommand(ctx, args...)
	var output tdlMediaOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	runErr := cmd.Run()
	if runErr == nil {
		return nil
	}
	cliOutput := string(output)
	err := p.tdlSessionError(ctx, runErr, cliOutput)
	if transferFailure(err).Code == "telegram_error" &&
		strings.Contains(strings.ToLower(cliOutput), "failed to get peer: failed to get result from") {
		return &Failure{Code: "access_denied"}
	}
	return err
}

func (p *tdlProvider) Download(
	ctx context.Context, link MessageLink, expected Media, target string, progress func(int64),
) error {
	if err := p.requireTDLSession(); err != nil {
		return err
	}
	if err := discardTDLTransferDirs(filepath.Dir(target)); err != nil {
		return &Failure{Code: "disk_write_failed"}
	}
	readContext, cancel := context.WithTimeout(ctx, 45*time.Second)
	verified, err := p.readTDLMedia(readContext, link, false)
	cancel()
	if err != nil {
		return err
	}
	if !tdlSameMedia(expected, verified.media) {
		return &Failure{Code: "media_changed"}
	}
	directory, err := os.MkdirTemp(filepath.Dir(target), ".tdl-")
	if err != nil {
		return &Failure{Code: "disk_write_failed"}
	}
	defer os.RemoveAll(directory)
	logTail, err := p.runTDLTransfer(ctx, verified, directory, progress)
	if err != nil {
		return err
	}

	ready := filepath.Join(directory, tdlTransferName)
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) != 1 || entries[0].Name() != tdlTransferName ||
		partSize(ready) != expected.Bytes {
		if reason := p.tdlSessionError(ctx, nil, logTail); reason != nil {
			return reason
		}
		return &Failure{Code: "download_incomplete"}
	}

	// The standalone CLI fetches the message again to download it. Recheck
	// the original link before exposing any bytes to the queue's save step.
	checkContext, stop := context.WithTimeout(ctx, 45*time.Second)
	after, err := p.readTDLMedia(checkContext, link, false)
	stop()
	if err != nil {
		return err
	}
	if after.channelID != verified.channelID || !tdlSameMedia(expected, after.media) {
		return &Failure{Code: "media_changed"}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := adoptTDLVideo(ready, target); err != nil {
		return &Failure{Code: "disk_write_failed"}
	}
	if progress != nil {
		progress(expected.Bytes)
	}
	return nil
}

func tdlSameMedia(a, b Media) bool {
	return a.ID == b.ID && a.Bytes == b.Bytes && a.HideThumbnail == b.HideThumbnail
}

func (p *tdlProvider) runTDLTransfer(
	parent context.Context, record tdlMediaRecord, directory string, progress func(int64),
) (string, error) {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	// The numeric URL is pinned to the inspected channel. The template
	// admits only the exact peer/message and the filename yielded by export.
	url := fmt.Sprintf("https://t.me/c/%d/%d", record.channelID, record.messageID)
	template := fmt.Sprintf(
		"{{if and (eq .DialogID %d) (eq .MessageID %d) (eq .FileName %s)}}%s{{else}}unexpected{{end}}",
		record.channelID, record.messageID, strconv.Quote(record.file), tdlTransferName,
	)
	cmd := p.tdlCommand(ctx, "download", "--url", url, "--dir", directory,
		"--template", template, "--restart", "--disable-progress-ps")
	var output tdlMediaOutput
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		return "", p.tdlSessionError(ctx, err, "")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if progress != nil {
		progress(0)
	}
	ticker := time.NewTicker(180 * time.Millisecond)
	defer ticker.Stop()
	partial := filepath.Join(directory, tdlTransferName+".tmp")
	lastBytes := int64(0)
	lastUpdate := time.Now()
	for {
		select {
		case err := <-done:
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if err != nil {
				return "", p.tdlSessionError(ctx, err, string(output))
			}
			return string(output), nil
		case <-ctx.Done():
			<-done
			return "", ctx.Err()
		case <-ticker.C:
			// tdlCommand pins --threads 1. The staging file grows in
			// order, so its byte size reflects actual downloaded bytes.
			if bytes := partSize(partial); bytes > lastBytes && bytes <= record.media.Bytes {
				lastBytes, lastUpdate = bytes, time.Now()
				if progress != nil {
					progress(min(bytes, record.media.Bytes-1))
				}
			}
			if time.Since(lastUpdate) > tdlTransferIdle {
				cancel()
				<-done
				return "", &Failure{Code: "telegram_network"}
			}
		}
	}
}

func adoptTDLVideo(source, target string) error {
	if stat, err := os.Lstat(target); err == nil {
		if !stat.Mode().IsRegular() {
			return errors.New("invalid staging file")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.OpenFile(source, os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(syncErr, closeErr); err != nil {
		return err
	}
	return os.Rename(source, target)
}

// discardTDLTransferDirs considers only app-created transient directories
// inside one private queue job. The destination selected by the user is
// never involved. It also leaves a preexisting symlink untouched.
func discardTDLTransferDirs(jobDir string) error {
	stagingDir, job := filepath.Split(filepath.Clean(jobDir))
	stagingDir = filepath.Clean(stagingDir)
	stagingInfo, err := os.Lstat(stagingDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !stagingInfo.IsDir() {
		return errors.New("invalid staging directory")
	}
	staging, err := os.OpenRoot(stagingDir)
	if err != nil {
		return err
	}
	defer staging.Close()
	if opened, err := staging.Stat("."); err != nil || !os.SameFile(stagingInfo, opened) {
		return errors.New("staging directory changed")
	}

	jobInfo, err := staging.Lstat(job)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !jobInfo.IsDir() {
		return errors.New("invalid download directory")
	}
	download, err := staging.OpenRoot(job)
	if err != nil {
		return err
	}
	defer download.Close()
	if opened, err := download.Stat("."); err != nil || !os.SameFile(jobInfo, opened) {
		return errors.New("download directory changed")
	}
	entries, err := fs.ReadDir(download.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".tdl-") {
			if err := download.RemoveAll(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

// CLI output is diagnostic only. Keep a bounded tail in memory and never
// forward raw logs or export data to the user interface.
type tdlMediaOutput []byte

func (b *tdlMediaOutput) Write(data []byte) (int, error) {
	const limit = 16 * 1024
	if len(data) >= limit {
		*b = append((*b)[:0], data[len(data)-limit:]...)
		return len(data), nil
	}
	if excess := len(*b) + len(data) - limit; excess > 0 {
		copy(*b, (*b)[excess:])
		*b = (*b)[:len(*b)-excess]
	}
	*b = append(*b, data...)
	return len(data), nil
}
