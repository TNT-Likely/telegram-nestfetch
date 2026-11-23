// SPDX-License-Identifier: MIT
// Package preview serves a single authorized media object to a local player.
package preview

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// BlockSize is the aligned byte count requested from Telegram.
	BlockSize   = 512 * 1024
	routePrefix = "/preview/"
)

// Serve advertises a local, private URL until ctx is cancelled. Each call to
// read retrieves one aligned Telegram block and may return the shorter tail.
// report receives the bytes and duration of each successful uncached read.
func Serve(ctx context.Context, size int64, contentType string,
	read func(context.Context, int64, []byte) (int, error), ready func(string) error,
	report func(int64, time.Duration),
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if size <= 0 || read == nil || !strings.HasPrefix(contentType, "video/") {
		return errors.New("invalid preview source")
	}
	file := &previewFile{size: size, read: read, report: report, offset: -1}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return err
	}
	host := listener.Addr().String()
	route := routePrefix + hex.EncodeToString(key)
	server := &http.Server{
		Handler:           handler(file, host, route, contentType),
		BaseContext:       func(net.Listener) context.Context { return ctx },
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       15 * time.Second,
		MaxHeaderBytes:    4096,
	}
	stop := context.AfterFunc(ctx, func() { _ = server.Close() })
	defer stop()
	defer server.Close()
	if err := ready("http://" + host + route); err != nil {
		return err
	}

	if err := server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func handler(file *previewFile, host, route, contentType string) http.Handler {
	active := make(chan struct{}, 2)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if r.Host != host || r.URL.RawQuery != "" || r.URL.ForceQuery ||
			subtle.ConstantTimeCompare([]byte(r.URL.EscapedPath()), []byte(route)) != 1 {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		select {
		case active <- struct{}{}:
			defer func() { <-active }()
		case <-r.Context().Done():
			return
		}

		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", "inline")
		section := io.NewSectionReader(previewReadAt{r.Context(), file}, 0, file.size)
		http.ServeContent(w, r, "", time.Time{}, section)
	})
}

type previewReadAt struct {
	ctx  context.Context
	file *previewFile
}

func (r previewReadAt) ReadAt(p []byte, offset int64) (int, error) {
	n := 0
	for len(p) > 0 && offset < r.file.size {
		count, err := r.file.readBlock(r.ctx, p, offset)
		if err != nil {
			return n, err
		}
		n += count
		offset += int64(count)
		p = p[count:]
	}
	if len(p) > 0 {
		return n, io.EOF
	}
	return n, nil
}

// A single bounded block is shared by the player's overlapping range requests.
type previewFile struct {
	size   int64
	read   func(context.Context, int64, []byte) (int, error)
	report func(int64, time.Duration)

	mu     sync.Mutex
	offset int64
	block  []byte
}

func (f *previewFile) readBlock(ctx context.Context, p []byte, offset int64) (int, error) {
	if offset < 0 {
		return 0, errors.New("invalid preview offset")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return 0, err
	}

	aligned := offset - offset%BlockSize
	if aligned != f.offset {
		ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		f.offset = -1
		if f.block == nil {
			f.block = make([]byte, BlockSize)
		}
		started := time.Now()
		n, err := f.read(ctx, aligned, f.block)
		elapsed := time.Since(started)
		if err != nil && err != io.EOF {
			return 0, err
		}
		if int64(n) != min(int64(BlockSize), f.size-aligned) {
			return 0, io.ErrUnexpectedEOF
		}
		f.offset = aligned
		if f.report != nil && elapsed > 0 && ctx.Err() == nil {
			f.report(int64(n), elapsed)
		}
	}
	valid := min(int64(BlockSize), f.size-aligned)
	return copy(p, f.block[offset-aligned:valid]), nil
}

// ContentType picks a safe video MIME type after the upstream metadata has
// been checked to describe a video.
func ContentType(telegramType, name string) string {
	if mediaType, _, err := mime.ParseMediaType(telegramType); err == nil && strings.HasPrefix(mediaType, "video/") {
		return mediaType
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".webm":
		return "video/webm"
	case ".mov":
		return "video/quicktime"
	case ".ogv":
		return "video/ogg"
	case ".mkv":
		return "video/x-matroska"
	case ".avi":
		return "video/x-msvideo"
	case ".mpeg", ".mpg":
		return "video/mpeg"
	default:
		return "video/mp4"
	}
}

// ValidURL checks the URL shape before a desktop frontend uses it.
func ValidURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" ||
		parsed.User != nil || parsed.RawPath != "" || parsed.Fragment != "" ||
		parsed.ForceQuery || parsed.RawQuery != "" {
		return false
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return false
	}
	token, ok := strings.CutPrefix(parsed.Path, routePrefix)
	if !ok || len(token) != 64 {
		return false
	}
	_, err = hex.DecodeString(token)
	return err == nil
}
