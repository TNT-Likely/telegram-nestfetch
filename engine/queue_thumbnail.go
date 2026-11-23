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
	fetch := entry != nil && entry.HasThumbnail
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
	output := filepath.Join(filepath.Dir(part), "frame.jpg")
	if input == output {
		output += ".jpg"
	}
	_ = os.Remove(input)
	_ = os.Remove(output)
	if err := os.Link(part, input); err != nil {
		return false
	}
	defer os.Remove(input)
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
	return q.saveThumbnail(entry.ID, "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString(data))
}
