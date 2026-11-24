// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestTelegramProxyAddressValidation(t *testing.T) {
	address, err := parseTelegramProxy(" socks5://user:p%40ss@[::1]:01080/ ")
	if err != nil {
		t.Fatal(err)
	}
	if actual := telegramProxyURL(address); actual != "socks5://user:p%40ss@[::1]:1080" {
		t.Fatalf("wrong normalized address: %q", actual)
	}
	if status := publicTelegramProxy(address); status.Address != "socks5://[::1]:1080" || !status.Authenticated {
		t.Fatalf("wrong public details: %+v", status)
	}
	for _, raw := range []string{
		"localhost:1080", "socks5://localhost", "socks5://localhost:0", "socks5://localhost:65536",
		"http://:8080", "http://localhost:8080/path", "http://localhost:8080/?redirect=anything",
		"http://localhost:8080#fragment", "http://user:pass%0Aword@localhost:8080",
		"socks5://user@localhost:1080", "http://user:secret%XX@localhost:8080",
		"http://user:" + strings.Repeat("你", 800) + "@localhost:8080", "https://localhost:443",
	} {
		_, err := parseTelegramProxy(raw)
		expectCode(t, err, "proxy_invalid")
		if err != nil && err.Error() != "proxy_invalid" {
			t.Fatal("proxy validation exposed the underlying input")
		}
	}
}

func TestTelegramProxyPersistsAndConfiguresTDL(t *testing.T) {
	const raw = "http://proxyUser:proxy%40Pass@localhost:8080"
	data, destination := t.TempDir(), t.TempDir()
	provider := fakeTelegram("disconnected")
	q, err := openQueue(context.Background(), data, destination, "", provider)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	changed, err := q.setProxy(raw)
	if err != nil || !changed {
		t.Fatalf("set proxy: changed=%v error=%v", changed, err)
	}
	changed, err = q.setProxy(raw + "/")
	if err != nil || changed {
		t.Fatalf("save identical proxy: changed=%v error=%v", changed, err)
	}
	if q.workNext() {
		t.Fatal("old sidecar started work during proxy change")
	}
	expectCode(t, q.prepareUpdate(), "update_busy")
	q.finishUpdate()
	expectCode(t, q.prepareUpdate(), "update_busy")
	file, err := os.Stat(filepath.Join(data, telegramProxyFile))
	if err != nil || (runtime.GOOS != "windows" && file.Mode().Perm() != 0600) {
		t.Fatalf("proxy configuration is not a private file: %v", err)
	}
	q.close()

	q, err = openQueue(context.Background(), data, destination, "", provider)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	snapshot := q.snapshot()
	status := snapshot.Settings.Proxy
	if status.Address != "http://localhost:8080" || !status.Authenticated {
		t.Fatalf("wrong proxy status after restart: %+v", status)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil || strings.Contains(string(encoded), "proxyUser") || strings.Contains(string(encoded), "proxy%40Pass") {
		t.Fatal("snapshot exposed proxy credentials")
	}

	t.Setenv("TDL_PROXY", "http://unexpected:8080")
	binary := filepath.Join(data, "tdl-fixture")
	if err := os.WriteFile(binary, nil, 0600); err != nil {
		t.Fatal(err)
	}
	tdl, err := newTDLProvider(data, binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, subcommand := range []string{"login", "chat", "download"} {
		cmd := tdl.tdlCommand(context.Background(), subcommand)
		settings := slices.DeleteFunc(slices.Clone(cmd.Env), func(value string) bool {
			return !strings.HasPrefix(value, "TDL_PROXY=")
		})
		if !slices.Equal(settings, []string{"TDL_PROXY=" + raw}) || strings.Contains(strings.Join(cmd.Args, " "), "proxy%40Pass") {
			t.Fatalf("tdl %s did not use the saved proxy exclusively via its environment", subcommand)
		}
	}
	native, err := newTelegram(data, APIConfig{})
	if err != nil || native.(*telegramProvider).resolver == nil {
		t.Fatalf("native Telegram provider did not set its resolver: %v", err)
	}
	native.Close()

	if changed, err := q.setProxy(""); err != nil || !changed {
		t.Fatalf("clear proxy: changed=%v error=%v", changed, err)
	}
	if address, err := readTelegramProxy(data); err != nil || address != nil {
		t.Fatalf("clear proxy did not persist direct mode: %v", err)
	}
}

func TestTelegramProxyProbeUsesCONNECTAndRedactsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect || r.Host != "149.154.167.41:443" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpzZWNyZXQ=" {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	address := strings.Replace(server.URL, "http://", "http://user:secret@", 1)
	data := t.TempDir()
	q, err := openQueue(context.Background(), data, t.TempDir(), "", fakeTelegram("disconnected"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if err := q.testProxy(context.Background(), &address); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(data, telegramProxyFile)); !os.IsNotExist(err) {
		t.Fatal("testing a proxy changed the saved configuration")
	}
	unauthenticated := server.URL
	expectCode(t, q.testProxy(context.Background(), &unauthenticated), "proxy_test_failed")
	if changed, err := q.setProxy(address); err != nil || !changed {
		t.Fatalf("save proxy: changed=%v error=%v", changed, err)
	}
	if err := q.testProxy(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}

func TestTelegramProxyHandshakeHonorsCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(release)
	address, err := parseTelegramProxy(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	dial, err := newTelegramProxyDialer(address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		conn, err := dial(ctx, "tcp", "127.0.0.1:80")
		if conn != nil {
			_ = conn.Close()
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("proxy never received the tunnel request")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("cancelled proxy handshake returned a connection")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("proxy handshake did not exit after cancellation")
	}
}

func TestTelegramProxyProbeUsesSOCKS5(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	result := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			err = acceptSOCKS5Probe(conn)
		}
		result <- err
	}()

	address := "socks5://user:p%40ss@" + listener.Addr().String()
	q := &queue{}
	if err := q.testProxy(context.Background(), &address); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
}

func acceptSOCKS5Probe(conn net.Conn) error {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	for step, exchange := range []struct{ request, response string }{
		{"\x05\x02\x00\x02", "\x05\x02"},
		{"\x01\x04user\x04p@ss", "\x01\x00"},
		{"\x05\x01\x00\x01\x95\x9a\xa7\x29\x01\xbb", "\x05\x00\x00\x01\x00\x00\x00\x00\x00\x00"},
	} {
		input := make([]byte, len(exchange.request))
		if _, err := io.ReadFull(conn, input); err != nil {
			return err
		}
		if string(input) != exchange.request {
			return fmt.Errorf("unexpected SOCKS5 request at step %d", step+1)
		}
		if _, err := io.WriteString(conn, exchange.response); err != nil {
			return err
		}
	}
	return nil
}

func TestQueueUpdateHoldPreservesQueuedDownloads(t *testing.T) {
	backend := fakeTelegram("qr")
	q, err := openQueue(context.Background(), t.TempDir(), t.TempDir(), "", backend)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	expectCode(t, q.prepareUpdate(), "update_busy")
	_, err = q.setProxy("http://localhost:8080")
	expectCode(t, err, "proxy_busy")

	backend.setAccount("disconnected")
	if err := q.prepareUpdate(); err != nil {
		t.Fatal(err)
	}
	backend.setAccount("connected")
	id, err := q.enqueue(mediaURL)
	if err != nil {
		t.Fatal(err)
	}
	if q.workNext() {
		t.Fatal("queue was processed while reserved for the updater")
	}
	expectState(t, q, id, queued)
	expectCode(t, q.prepareUpdate(), "update_busy")
	expectCode(t, q.accountAction(backend.StartLogin), "busy")
	q.finishUpdate()
	expectState(t, q, id, completed)
}

func TestQueueUpdateAndProxyRejectActiveTransfer(t *testing.T) {
	backend := fakeTelegram("connected")
	backend.pauseFirst = true
	backend.reachedPart = make(chan struct{})
	q, err := openQueue(context.Background(), t.TempDir(), t.TempDir(), "", backend)
	if err != nil {
		t.Fatal(err)
	}
	defer q.close()
	if _, err := q.enqueue(mediaURL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-backend.reachedPart:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not reach the pause point")
	}
	expectCode(t, q.prepareUpdate(), "update_busy")
	_, err = q.setProxy("socks5://localhost:1080")
	expectCode(t, err, "proxy_busy")
}
