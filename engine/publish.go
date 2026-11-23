// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

const tempPrefix = ".tg-nestfetch-"

type saveIntent struct {
	Temporary string `json:"temporary"`
	Name      string `json:"name,omitempty"`
	Digest    string `json:"digest,omitempty"`
}

func (s *saveIntent) valid() bool {
	id := strings.TrimSuffix(strings.TrimPrefix(s.Temporary, tempPrefix), ".part")
	if s.Temporary != tempPrefix+id+".part" || !validID(id) {
		return false
	}
	if s.Name == "" && s.Digest == "" {
		return true
	}
	if !validOutputName(s.Name) || len(s.Digest) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(s.Digest)
	return err == nil
}

type folderHandle struct {
	path string
	root *os.Root
	dir  *os.File
}

func openFolder(raw string) (*folderHandle, error) {
	if !filepath.IsAbs(raw) || strings.ContainsRune(raw, '\x00') {
		return nil, &Failure{Code: "invalid_folder"}
	}
	path := filepath.Clean(raw)
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, &Failure{Code: "folder_unavailable"}
	}
	dir, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, &Failure{Code: "folder_unavailable"}
	}
	folder := &folderHandle{path: path, root: root, dir: dir}
	if !folderAvailable(path, dir) {
		folder.close()
		return nil, &Failure{Code: "folder_unavailable"}
	}
	return folder, nil
}

func (folder *folderHandle) close() {
	_ = folder.dir.Close()
	_ = folder.root.Close()
}

func checkFolder(path string) (string, error) {
	folder, err := openFolder(path)
	if err != nil {
		return "", err
	}
	defer folder.close()
	return folder.path, nil
}

func removeTemporary(path string, temp string) {
	if folder, err := openFolder(path); err == nil {
		defer folder.close()
		_ = folder.root.Remove(temp)
	}
}

func filename(raw string) string {
	// Telegram filenames are untrusted. Use one portable leaf name and leave
	// room for a collision suffix and the filesystem's own encoding.
	safe := strings.Trim(strings.Map(func(r rune) rune {
		if excludedFilenameRune(r) {
			return '_'
		}
		return r
	}, raw), ". ")
	if safe == "" {
		return "video.mp4"
	}
	if base, _, _ := strings.Cut(safe, "."); isDeviceName(base) {
		safe = "_" + safe
	}
	if len(safe) <= 195 {
		return safe
	}
	ext := filepath.Ext(safe)
	if len(ext) > 20 {
		ext = ""
	}
	return strings.TrimRight(shorten(strings.TrimSuffix(safe, ext), 195-len(ext)), ". ") + ext
}

func validOutputName(name string) bool {
	if name == "" || len(name) > 215 || strings.Trim(name, ". ") != name ||
		strings.ContainsFunc(name, excludedFilenameRune) {
		return false
	}
	base, _, _ := strings.Cut(name, ".")
	return !isDeviceName(base)
}

func excludedFilenameRune(r rune) bool {
	return unicode.IsControl(r) || strings.ContainsRune("/\\:*?\"<>|", r) || r == utf8.RuneError
}

func shorten(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}

func isDeviceName(value string) bool {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$",
		"COM0", "COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9", "COM¹", "COM²", "COM³",
		"LPT0", "LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9", "LPT¹", "LPT²", "LPT³":
		return true
	default:
		return false
	}
}

func candidateName(name string, attempt int) string {
	if attempt == 1 {
		return name
	}
	ext := filepath.Ext(name)
	return fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), attempt, ext)
}

func publishFailure(err error) *Failure {
	var failure *Failure
	if errors.As(err, &failure) {
		return failure
	}
	return &Failure{Code: "save_failed", Message: err.Error()}
}

func (q *queue) publish(ctx context.Context, entry queueEntry) error {
	folder, err := openFolder(entry.Folder)
	if err != nil {
		return err
	}
	defer folder.close()

	temp, digest, recovered, err := q.prepareOutput(ctx, entry, folder)
	if err != nil {
		return publishFailure(err)
	}
	if recovered != "" {
		return q.finishOutput(ctx, entry.ID, folder, recovered, temp)
	}

	for attempt := 1; attempt <= 1000; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		name := candidateName(entry.Name, attempt)
		if _, err := folder.root.Lstat(name); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return publishFailure(err)
		}
		intent := &saveIntent{Temporary: temp, Name: name, Digest: digest}
		if !q.jobUpdate(ctx, entry.ID, func(job *queueEntry) { job.Save = intent }) {
			return &Failure{Code: "local_storage"}
		}
		// Reconfirm the selected path still refers to the opened directory.
		// A mounted destination must never resolve to its local shadow path.
		if !folderAvailable(folder.path, folder.dir) {
			return &Failure{Code: "folder_unavailable"}
		}
		err := commitOutput(folder, temp, name)
		switch {
		case errors.Is(err, os.ErrExist):
			continue
		case err != nil:
			return publishFailure(err)
		default:
			return q.finishOutput(ctx, entry.ID, folder, name, temp)
		}
	}
	return &Failure{Code: "save_failed", Message: "no unused filename found"}
}

func (q *queue) prepareOutput(ctx context.Context, entry queueEntry, folder *folderHandle) (temp, digest, recovered string, err error) {
	if previous := entry.Save; previous != nil {
		// The previous process may have exited between the atomic move and the
		// queue commit. Verify content, so an unrelated output is never adopted.
		if previous.Digest != "" && matchesOutput(ctx, folder.root, previous.Name, entry.Size, previous.Digest) {
			return previous.Temporary, previous.Digest, previous.Name, nil
		}
		if previous.Digest != "" && matchesOutput(ctx, folder.root, previous.Temporary, entry.Size, previous.Digest) {
			return previous.Temporary, previous.Digest, "", nil
		}
		if err := folder.root.Remove(previous.Temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", "", err
		}
	}
	if err := ctx.Err(); err != nil {
		return "", "", "", err
	}
	id, err := randomID()
	if err != nil {
		return "", "", "", err
	}
	temp = tempPrefix + id + ".part"
	intent := &saveIntent{Temporary: temp}
	if !q.jobUpdate(ctx, entry.ID, func(job *queueEntry) { job.Save = intent }) {
		return "", "", "", &Failure{Code: "local_storage"}
	}
	digest, err = copyOutput(ctx, q.partPath(entry.ID), folder.root, temp, entry.Size)
	return temp, digest, "", err
}

func (q *queue) finishOutput(ctx context.Context, id string, folder *folderHandle, name, temp string) error {
	// An exclusive hard link may leave the temporary hard link in place.
	_ = folder.root.Remove(temp)
	_ = folder.dir.Sync()
	if !q.jobUpdate(ctx, id, func(job *queueEntry) {
		job.State, job.Error, job.Save = completed, nil, nil
		job.OutputPath = filepath.Join(folder.path, name)
	}) {
		return &Failure{Code: "local_storage"}
	}
	_ = os.Remove(q.partPath(id))
	_ = os.Remove(filepath.Dir(q.partPath(id)))
	return nil
}

func matchesOutput(ctx context.Context, root *os.Root, name string, size int64, digest string) bool {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return false
	}
	file, err := root.Open(name)
	if err != nil {
		return false
	}
	defer file.Close()
	hash := sha256.New()
	count, err := io.Copy(hash, contextualReader{ctx, io.LimitReader(file, size+1)})
	return err == nil && count == size && hex.EncodeToString(hash.Sum(nil)) == digest
}

func copyOutput(ctx context.Context, source string, root *os.Root, name string, size int64) (string, error) {
	sourceFile, err := os.Open(source)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", &Failure{Code: "download_incomplete"}
		}
		return "", err
	}
	defer sourceFile.Close()
	info, err := sourceFile.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return "", &Failure{Code: "download_incomplete"}
	}
	output, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	count, err := io.Copy(io.MultiWriter(output, hash), contextualReader{ctx, io.LimitReader(sourceFile, size+1)})
	if err == nil && count != size {
		err = &Failure{Code: "download_incomplete"}
	}
	if err == nil {
		err = output.Sync()
	}
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = root.Remove(name)
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type contextualReader struct {
	ctx context.Context
	src io.Reader
}

func (r contextualReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.src.Read(data)
}

func sameOpenedFolder(path string, dir *os.File) bool {
	atPath, err := os.Stat(path)
	if err != nil || !atPath.IsDir() {
		return false
	}
	opened, err := dir.Stat()
	return err == nil && os.SameFile(atPath, opened)
}
