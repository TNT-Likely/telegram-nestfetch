// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
)

type tdlProvider struct {
	lifecycle sync.Mutex
	mu        sync.Mutex

	dir     string
	home    string
	binary  string
	account Account
	login   *tdlLogin
}

type tdlLogin struct {
	ctx           context.Context
	cancel        context.CancelFunc
	done          chan struct{}
	command       *exec.Cmd
	terminal      tdlTerminal
	prompt        bool     // guarded by the provider mutex
	cursorQueries int      // guarded by the provider mutex
	password      bool     // guarded by the provider mutex
	failure       *Failure // guarded by the provider mutex
}

type tdlVerifiedAccount struct {
	Version int    `json:"version"`
	Label   string `json:"label,omitempty"`
}

func newTDLProvider(dataDir, binary string) (*tdlProvider, error) {
	if !filepath.IsAbs(dataDir) || !filepath.IsAbs(binary) {
		return nil, telegramFailure("tdl_unavailable")
	}
	if info, err := os.Stat(binary); err != nil || !info.Mode().IsRegular() {
		return nil, telegramFailure("tdl_unavailable")
	}

	dir := filepath.Join(dataDir, "tdl")
	p := &tdlProvider{
		dir: dir, home: filepath.Join(dir, "home"), binary: binary, account: tdlAccount("disconnected"),
	}
	for _, path := range []string{p.dir, p.home, filepath.Join(p.dir, "data")} {
		if err := ensureTDLDirectory(path); err != nil {
			return nil, telegramFailure("disk_write_failed")
		}
	}
	if account, ok := readTDLVerification(dataDir); ok {
		p.account = tdlAccount("connected")
		p.account.Label = account.Label
	}
	return p, nil
}

func ensureTDLDirectory(path string) error {
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("tdl data location is not an owned directory")
	}
	return os.Chmod(path, 0700)
}

func tdlAccount(state string) Account {
	return Account{State: state, Backend: "tdl", Preconfigured: true}
}

func tdlHasSession(dataDir string) bool {
	_, ok := readTDLVerification(dataDir)
	return ok
}

func readTDLVerification(dataDir string) (tdlVerifiedAccount, bool) {
	var record tdlVerifiedAccount
	dir := filepath.Join(dataDir, "tdl")
	for _, path := range []string{dir, filepath.Join(dir, "data")} {
		if info, err := os.Lstat(path); err != nil || !info.IsDir() {
			return record, false
		}
	}
	db, err := os.Lstat(filepath.Join(dir, "data", tdlNamespace))
	if err != nil || !db.Mode().IsRegular() || db.Size() == 0 {
		return record, false
	}
	marker := filepath.Join(dir, "verified.json")
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024 {
		return record, false
	}
	data, err := os.ReadFile(marker)
	if err != nil || json.Unmarshal(data, &record) != nil {
		return record, false
	}
	return record, record.Version == 1
}

func (p *tdlProvider) Status() Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.account
}

func (p *tdlProvider) Configure(APIConfig) error { return telegramFailure("invalid_request") }

func (p *tdlProvider) StartLogin() error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()

	p.mu.Lock()
	active := p.login != nil || p.account.State == "connected"
	p.mu.Unlock()
	if active {
		return telegramFailure("login_in_progress")
	}
	if err := p.clearTDLStorage(); err != nil {
		return err
	}
	db, err := os.OpenFile(p.dbPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return telegramFailure("disk_write_failed")
	}
	if err := db.Close(); err != nil {
		return telegramFailure("disk_write_failed")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	cmd := p.tdlCommand(ctx, "login", "-T", "qr")
	terminal, err := startTDLTerminal(cmd)
	if err != nil {
		cancel()
		_ = p.clearTDLStorage()
		return tdlCommandFailure(context.Background(), err, "")
	}

	login := &tdlLogin{
		ctx: ctx, cancel: cancel, done: make(chan struct{}), command: cmd, terminal: terminal,
	}
	p.mu.Lock()
	p.login = login
	p.account = tdlAccount("starting")
	p.mu.Unlock()
	go p.runTDLLogin(login)
	return nil
}

func (p *tdlProvider) runTDLLogin(login *tdlLogin) {
	defer close(login.done)
	defer login.cancel()

	read := make(chan tdlLoginResult, 1)
	go func() {
		read <- readTDLLoginOutput(login.terminal.output, func(event tdlLoginEvent) {
			p.updateTDLLogin(login, event)
		})
	}()
	runErr := login.command.Wait()

	var result tdlLoginResult
	select {
	case result = <-read:
	case <-time.After(750 * time.Millisecond):
		_ = login.terminal.output.Close()
	}
	_ = login.terminal.output.Close()

	p.mu.Lock()
	if p.login != login {
		p.mu.Unlock()
		return
	}

	err := tdlCommandFailure(login.ctx, runErr, result.output)
	var failure *Failure
	switch {
	case login.failure != nil:
		failure = login.failure
	case errors.Is(err, context.DeadlineExceeded):
		failure = telegramFailure("telegram_network")
	case errors.As(err, &failure):
	case err != nil || !result.accepted:
		failure = telegramFailure("telegram_error")
	case p.saveTDLVerification(result.label) != nil:
		failure = telegramFailure("disk_write_failed")
	}

	p.login = nil
	if failure == nil {
		p.account = tdlAccount("connected")
		p.account.Label = result.label
		p.mu.Unlock()
		return
	}
	p.account = tdlAccount("error")
	p.account.Error = failure
	_ = p.clearTDLStorage()
	p.mu.Unlock()
}

func (p *tdlProvider) updateTDLLogin(login *tdlLogin, event tdlLoginEvent) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.login != login {
		return
	}
	if event.password {
		if !login.terminal.interactive {
			login.failure = telegramFailure("tdl_password_unsupported")
			login.cancel()
			return
		}
		login.prompt = true
		p.account = tdlAccount("starting")
	} else if event.cursorReport && login.prompt {
		// survey first probes the terminal dimensions from the bottom
		// right, then the current cursor location before reading input.
		login.cursorQueries++
		reply := "\x1b[1;1R"
		if login.cursorQueries == 1 {
			reply = fmt.Sprintf("\x1b[%d;%dR", tdlTerminalRows, tdlTerminalCols)
		}
		if n, err := login.terminal.write([]byte(reply)); err != nil || n != len(reply) {
			login.failure = telegramFailure("telegram_error")
			login.cancel()
			return
		}
		if login.cursorQueries == 2 {
			login.password = true
			p.account = tdlAccount("password")
		}
	} else if event.qr != "" {
		p.account = tdlAccount("qr")
		p.account.QRImage = event.qr
		// Each refreshed frame replaces the previous one. The CLI does not
		// report a time to display as an expiry countdown.
	}
}

func (p *tdlProvider) SubmitPassword(password string) error {
	if password == "" || len(password) > 4096 || strings.ContainsFunc(password, unicode.IsControl) {
		return telegramFailure("password_invalid")
	}
	p.mu.Lock()
	login := p.login
	if login == nil || !login.password {
		p.mu.Unlock()
		return telegramFailure("login_not_pending")
	}
	login.password = false
	p.mu.Unlock()

	line := append([]byte(password), '\r')
	defer clear(line)
	if n, err := login.terminal.write(line); err != nil || n != len(line) {
		return telegramFailure("login_not_pending")
	}
	return nil
}

func (p *tdlProvider) CancelLogin() error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	login := p.detachTDLLogin()
	if login == nil {
		return telegramFailure("login_not_pending")
	}
	stopTDLLogin(login)
	return p.clearTDLStorage()
}

func (p *tdlProvider) SignOut(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()

	stopTDLLogin(p.detachTDLLogin())
	if err := p.clearTDLStorage(); err != nil {
		return err
	}
	p.mu.Lock()
	p.account = tdlAccount("disconnected")
	p.mu.Unlock()
	return nil
}

func (p *tdlProvider) Close() {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	if login := p.detachTDLLogin(); login != nil {
		stopTDLLogin(login)
		_ = p.clearTDLStorage()
	}
}

func (p *tdlProvider) detachTDLLogin() *tdlLogin {
	p.mu.Lock()
	defer p.mu.Unlock()
	login := p.login
	if login != nil {
		p.login = nil
		p.account = tdlAccount("disconnected")
	}
	return login
}

func stopTDLLogin(login *tdlLogin) {
	if login != nil {
		login.cancel()
		<-login.done
	}
}

func (p *tdlProvider) saveTDLVerification(label string) error {
	info, err := os.Lstat(p.dbPath())
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
		return telegramFailure("disk_write_failed")
	}
	if err := os.Chmod(p.dbPath(), 0600); err != nil {
		return err
	}
	return writePrivateFile(p.markerPath(), func(w io.Writer) error {
		return json.NewEncoder(w).Encode(tdlVerifiedAccount{Version: 1, Label: label})
	})
}

func (p *tdlProvider) clearTDLStorage() error {
	for _, path := range []string{p.markerPath(), p.dbPath()} {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return telegramFailure("disk_write_failed")
		}
	}
	return nil
}

func (p *tdlProvider) dbPath() string { return filepath.Join(p.dir, "data", tdlNamespace) }

func (p *tdlProvider) markerPath() string { return filepath.Join(p.dir, "verified.json") }
