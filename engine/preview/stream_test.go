// SPDX-License-Identifier: MIT
package preview

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestPreviewRangesAndAccess(t *testing.T) {
	content := make([]byte, BlockSize+29)
	for i := range content {
		content[i] = byte(i % 251)
	}
	const transferDelay = 2 * time.Millisecond
	var reads, reports atomic.Int32
	var totalBytes atomic.Int64
	read := func(_ context.Context, offset int64, block []byte) (int, error) {
		if offset%BlockSize != 0 || len(block) != BlockSize {
			t.Errorf("incorrect Telegram chunk: offset %d, size %d", offset, len(block))
		}
		reads.Add(1)
		time.Sleep(transferDelay)
		return copy(block, content[offset:]), nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	open := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, int64(len(content)), "video/mp4", read, func(addr string) error {
			open <- addr
			return nil
		}, func(count int64, elapsed time.Duration) {
			if elapsed < transferDelay {
				t.Errorf("reported duration %s excludes the upstream read", elapsed)
			}
			reports.Add(1)
			totalBytes.Add(count)
		})
	}()
	var addr string
	select {
	case addr = <-open:
	case err := <-done:
		t.Fatalf("stream failed to start: %v", err)
	case <-ctx.Done():
		t.Fatal("stream did not start")
	}
	if !ValidURL(addr) {
		t.Fatal("stream emitted an invalid URL")
	}

	client := &http.Client{Timeout: 2 * time.Second}
	get := func(method, address, host, rangeValue string, code int) ([]byte, http.Header) {
		t.Helper()
		req, err := http.NewRequest(method, address, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Host = host
		req.Header.Set("Range", rangeValue)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != code {
			t.Fatalf("unexpected HTTP status for %s %s: %d, want %d", method, rangeValue, res.StatusCode, code)
		}
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		return body, res.Header
	}

	get(http.MethodGet, addr+"0", "", "", http.StatusNotFound)
	get(http.MethodGet, addr, "evil.example", "", http.StatusNotFound)
	get(http.MethodGet, addr+"?unused=1", "", "", http.StatusNotFound)
	get(http.MethodPost, addr, "", "", http.StatusMethodNotAllowed)
	body, hdr := get(http.MethodHead, addr, "", "", http.StatusOK)
	if len(body) != 0 || hdr.Get("Content-Length") != fmt.Sprint(len(content)) ||
		hdr.Get("Cache-Control") != "no-store" || hdr.Get("Accept-Ranges") != "bytes" || reads.Load() != 0 || reports.Load() != 0 {
		t.Fatal("HEAD or an unauthorized request fetched media, or stream metadata was incorrect")
	}

	body, hdr = get(http.MethodGet, addr, "", "bytes=0-1", http.StatusPartialContent)
	if !bytes.Equal(body, content[:2]) || hdr.Get("Content-Range") != fmt.Sprintf("bytes 0-1/%d", len(content)) ||
		hdr.Get("Content-Type") != "video/mp4" || reads.Load() != 1 || reports.Load() != 1 || totalBytes.Load() != BlockSize {
		t.Fatal("the two-byte probe did not serve its exact range or report exactly one upstream block")
	}
	boundary := fmt.Sprintf("bytes=%d-%d", BlockSize-2, BlockSize+1)
	body, _ = get(http.MethodGet, addr, "", boundary, http.StatusPartialContent)
	if !bytes.Equal(body, content[BlockSize-2:BlockSize+2]) || reads.Load() != 2 {
		t.Fatal("range spanning chunks did not match the file or reuse the cached first chunk")
	}
	body, _ = get(http.MethodGet, addr, "", "bytes=-3", http.StatusPartialContent)
	if !bytes.Equal(body, content[len(content)-3:]) || reads.Load() != 2 ||
		reports.Load() != 2 || totalBytes.Load() != int64(len(content)) {
		t.Fatal("suffix range had incorrect bytes or reported cached data again")
	}
	_, hdr = get(http.MethodGet, addr, "", fmt.Sprintf("bytes=%d-", len(content)), http.StatusRequestedRangeNotSatisfiable)
	if hdr.Get("Content-Range") != fmt.Sprintf("bytes */%d", len(content)) || reads.Load() != 2 {
		t.Fatal("out-of-bounds range reached Telegram")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("stream stopped with error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closed preview retained its server")
	}
	res, err := client.Get(addr)
	if err == nil {
		res.Body.Close()
		t.Fatal("a closed preview URL still allowed connections")
	}
}

func TestValidPreviewURL(t *testing.T) {
	key := strings.Repeat("a", 64)
	host := "http://127.0.0.1:5555"
	for _, address := range []string{
		host + "/frame/" + key,
		host + "/preview/" + key[:63],
		host + "/preview/" + key + "?",
		host + "/preview/" + key + "#fragment",
		"http://localhost:5555/preview/" + key,
		"http://user@127.0.0.1:5555/preview/" + key,
		"http://127.0.0.1:0/preview/" + key,
		"https://127.0.0.1:5555/preview/" + key,
	} {
		if ValidURL(address) {
			t.Errorf("accepted invalid preview URL %s", address)
		}
	}
	if !ValidURL(host + "/preview/" + key) {
		t.Fatal("rejected the intended preview URL")
	}
}
