// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxThumbnailBytes = 384 * 1024
const maxThumbnailURL = 4*((maxThumbnailBytes+2)/3) + 32

func (q *queue) thumbnailPath(id string) string {
	return filepath.Join(q.dataDir, "thumbnails", id+".txt")
}

func (q *queue) thumbnail(id string) (any, error) {
	q.mu.Lock()
	entry := q.byIDLocked(id)
	fetch := entry != nil && entry.HasThumbnail && !entry.HideThumbnail
	q.mu.Unlock()
	if entry == nil {
		return nil, &Failure{Code: "not_found"}
	}
	if !fetch {
		return nil, nil
	}
	data, err := boundedRead(q.thumbnailPath(id), maxThumbnailURL)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, localFailure(err)
	}
	url := string(data)
	if !validThumbnail(url) {
		return nil, nil
	}
	return url, nil
}

func (q *queue) saveThumbnail(id, url string) bool {
	if !validThumbnail(url) {
		return false
	}
	err := writePrivateFile(q.thumbnailPath(id), func(writer io.Writer) error {
		_, err := io.WriteString(writer, url)
		return err
	})
	return err == nil
}

func (entry *queueEntry) localThumbnailAllowed() bool {
	return !entry.HideThumbnail && !entry.HasThumbnail && (entry.Kind == MediaVideo || entry.Kind == MediaImage)
}

func boundedRead(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("thumbnail data exceeds size limit")
	}
	return data, err
}

func validThumbnail(url string) bool {
	if len(url) > maxThumbnailURL {
		return false
	}
	kind, encoded, ok := strings.Cut(url, ";base64,")
	if !ok {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(data) > maxThumbnailBytes {
		return false
	}
	switch kind {
	case "data:image/jpeg":
		return bytes.HasPrefix(data, []byte{0xff, 0xd8, 0xff})
	case "data:image/png":
		return bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n"))
	case "data:image/webp":
		return len(data) >= 12 && string(data[:4]) == "RIFF" && string(data[8:12]) == "WEBP"
	default:
		return false
	}
}

func (q *queue) extractThumbnail(ctx context.Context, entry queueEntry) bool {
	if ctx.Err() != nil {
		return false
	}
	part := q.partPath(entry.ID)
	ext := filepath.Ext(entry.Name)
	if ext == "" || len(ext) > 12 {
		ext = ".mp4"
	}
	// Give native decoders an extension. Creating a hard link here never
	// duplicates a large video, and the temporary aliases are app-owned.
	input := filepath.Join(filepath.Dir(part), "frame-source"+ext)
	_ = os.Remove(input)
	if err := os.Link(part, input); err != nil {
		return false
	}
	defer os.Remove(input)
	return q.saveLocalPreview(ctx, entry.ID, input)
}

func (q *queue) saveLocalPreview(ctx context.Context, id, input string) bool {
	// Inputs can live on a read-only volume. All generated files stay in
	// the app's private cache, regardless of the selected download folder.
	output := filepath.Join(q.dataDir, "thumbnails", id+".frame.jpg")
	_ = os.Remove(output)
	defer os.Remove(output)
	timeout, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if err := exec.CommandContext(timeout, q.frameHelper, input, output).Run(); err != nil {
		return false
	}
	data, err := boundedRead(output, maxThumbnailBytes)
	if err != nil {
		return false
	}
	return q.saveThumbnail(id, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(data))
}

// Called before the download worker starts and before the queue is exposed to IPC.
func (q *queue) startThumbnailRecovery() {
	if q.frameHelper == "" {
		return
	}
	var pending []*queueEntry
	for i := len(q.state.Jobs) - 1; i >= 0; i-- {
		entry := q.state.Jobs[i]
		if entry.State == completed && entry.localThumbnailAllowed() && entry.OutputPath != "" && entry.Size > 0 {
			pending = append(pending, entry)
		}
	}
	if len(pending) > 0 {
		q.workers.Go(func() { q.restoreThumbnails(pending) })
	}
}

func (q *queue) restoreThumbnails(pending []*queueEntry) {
	for {
		offline := pending[:0]
		available := make(map[string]bool)
		for _, entry := range pending {
			if q.ctx.Err() != nil {
				return
			}
			mounted, checked := available[entry.Folder]
			if !checked {
				_, err := checkFolder(entry.Folder)
				mounted = err == nil
				available[entry.Folder] = mounted
			}
			if !mounted {
				offline = append(offline, entry)
				continue
			}
			// Completed entries are immutable except for the cached preview.
			// Skip a missing, replaced, or non-regular output.
			if partSize(entry.OutputPath) == entry.Size && q.saveLocalPreview(q.ctx, entry.ID, entry.OutputPath) {
				q.mu.Lock()
				_ = q.updateLocked(entry, func(job *queueEntry) { job.HasThumbnail = true })
				q.mu.Unlock()
			}
		}
		pending = offline
		if len(pending) == 0 {
			return
		}
		select {
		case <-q.ctx.Done():
			return
		case <-time.After(time.Minute):
			// A previously selected network volume may now be mounted.
		}
	}
}
