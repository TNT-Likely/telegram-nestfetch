// SPDX-License-Identifier: MIT
package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gotd/td/telegram/dcs"
	"golang.org/x/net/proxy"
)

const (
	telegramProxyFile = "telegram-proxy"
	proxyURLLimit     = 4096
	proxyDialTimeout  = 8 * time.Second
)

type telegramProxyStatus struct {
	Address       string `json:"address,omitempty"`
	Authenticated bool   `json:"authenticated,omitempty"`
}

func parseTelegramProxy(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	if len(raw) > proxyURLLimit || strings.ContainsFunc(raw, unicode.IsControl) {
		return nil, telegramFailure("proxy_invalid")
	}
	address, err := url.Parse(raw)
	if err != nil || (address.Scheme != "socks5" && address.Scheme != "http") ||
		address.Opaque != "" || address.ForceQuery || address.RawQuery != "" || address.Fragment != "" ||
		(address.Path != "" && address.Path != "/") {
		return nil, telegramFailure("proxy_invalid")
	}
	host, port, err := net.SplitHostPort(address.Host)
	number, portErr := strconv.ParseUint(port, 10, 16)
	if err != nil || host == "" || portErr != nil || number == 0 || strings.ContainsFunc(host, unicode.IsSpace) {
		return nil, telegramFailure("proxy_invalid")
	}
	if address.User != nil {
		username := address.User.Username()
		password, _ := address.User.Password()
		if username == "" || strings.ContainsFunc(username, unicode.IsControl) || strings.ContainsFunc(password, unicode.IsControl) ||
			(address.Scheme == "http" && strings.ContainsRune(username, ':')) ||
			(address.Scheme == "socks5" && (len(username) > 255 || len(password) == 0 || len(password) > 255)) {
			return nil, telegramFailure("proxy_invalid")
		}
	}
	address.Host = net.JoinHostPort(host, strconv.FormatUint(number, 10))
	address.Path, address.RawPath = "", ""
	if len(address.String()) > proxyURLLimit {
		return nil, telegramFailure("proxy_invalid")
	}
	return address, nil
}

func readTelegramProxy(dataDir string) (*url.URL, error) {
	data, err := boundedRead(filepath.Join(dataDir, telegramProxyFile), proxyURLLimit)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, telegramFailure("local_storage")
	}
	return parseTelegramProxy(string(data))
}

func publicTelegramProxy(address *url.URL) telegramProxyStatus {
	if address == nil {
		return telegramProxyStatus{}
	}
	public := *address
	public.User = nil
	return telegramProxyStatus{Address: public.String(), Authenticated: address.User != nil}
}

func telegramProxyURL(address *url.URL) string {
	if address == nil {
		return ""
	}
	return address.String()
}

func newTelegramProxyDialer(address *url.URL) (dcs.DialFunc, error) {
	direct := &net.Dialer{Timeout: proxyDialTimeout}
	if address == nil {
		return direct.DialContext, nil
	}
	if address.Scheme == "http" {
		return func(ctx context.Context, _, destination string) (net.Conn, error) {
			return dialHTTPProxy(ctx, direct, address, destination)
		}, nil
	}
	dialer, err := proxy.FromURL(address, direct)
	if err != nil {
		return nil, telegramFailure("proxy_invalid")
	}
	return dialer.(proxy.ContextDialer).DialContext, nil
}

type httpProxyConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *httpProxyConn) Read(p []byte) (int, error) { return c.reader.Read(p) }

func dialHTTPProxy(parent context.Context, direct *net.Dialer, address *url.URL, destination string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(parent, proxyDialTimeout)
	defer cancel()
	conn, err := direct.DialContext(ctx, "tcp", address.Host)
	if err != nil {
		return nil, err
	}
	connected := false
	defer func() {
		if !connected {
			_ = conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	req := &http.Request{Method: http.MethodConnect, URL: &url.URL{Host: destination}, Host: destination, Header: make(http.Header)}
	if address.User != nil {
		password, _ := address.User.Password()
		credentials := base64.StdEncoding.EncodeToString([]byte(address.User.Username() + ":" + password))
		req.Header.Set("Proxy-Authorization", "Basic "+credentials)
	}
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, errors.New("proxy rejected the tunnel")
	}
	if !stop() || ctx.Err() != nil {
		return nil, ctx.Err()
	}
	connected = true
	return &httpProxyConn{Conn: conn, reader: reader}, nil
}

func (q *queue) setProxy(raw string) (bool, error) {
	address, err := parseTelegramProxy(raw)
	if err != nil {
		return false, err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	value := telegramProxyURL(address)
	if value == telegramProxyURL(q.proxyAddress) {
		return false, nil
	}
	if q.engineBusyLocked() {
		return false, telegramFailure("proxy_busy")
	}
	if err := writePrivateFile(filepath.Join(q.dataDir, telegramProxyFile), func(writer io.Writer) error {
		_, err := io.WriteString(writer, value)
		return err
	}); err != nil {
		return false, telegramFailure("local_storage")
	}
	q.proxyAddress = address
	// Rust restarts the sidecar while holding the IPC lock. Keep the old
	// provider from starting work with its original settings in the meantime.
	q.hold = holdForProxy
	return true, nil
}

func (q *queue) testProxy(parent context.Context, raw *string) error {
	var address *url.URL
	if raw != nil {
		var err error
		address, err = parseTelegramProxy(*raw)
		if err != nil {
			return err
		}
	} else {
		q.mu.Lock()
		address = q.proxyAddress
		q.mu.Unlock()
	}
	dial, err := newTelegramProxyDialer(address)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parent, proxyDialTimeout)
	defer cancel()
	// Probe gotd's default production DC without starting a Telegram session.
	for _, dc := range dcs.Prod().Options {
		if dc.ID != 2 || dc.Ipv6 || dc.MediaOnly || dc.CDN {
			continue
		}
		conn, err := dial(ctx, "tcp", net.JoinHostPort(dc.IPAddress, strconv.Itoa(dc.Port)))
		if err != nil {
			break
		}
		_ = conn.Close()
		return nil
	}
	return telegramFailure("proxy_test_failed")
}
