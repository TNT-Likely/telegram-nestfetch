// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

const (
	connectDeadline = 25 * time.Second
	loginDeadline   = 5 * time.Minute
)

var errNoTelegramSession = errors.New("no Telegram session")

type telegramRun struct {
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	passwords  chan string
	accepting  bool // guarded by the provider mutex
	hadSession bool
	client     *telegram.Client
}

type telegramProvider struct {
	lifecycle     sync.Mutex
	mu            sync.Mutex
	config        APIConfig
	account       Account
	preconfigured bool
	run           *telegramRun
	apiPath       string
	storage       *telegramStorage
}

var _ Telegram = (*telegramProvider)(nil)

func desktopTelegram(dataDir, tdlBinary string, defaults APIConfig) (Telegram, error) {
	info, err := os.Stat(tdlBinary)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	bundled := err == nil && info.Mode().IsRegular()
	// A later release can add a registered API app without forcing tdl users
	// to authorize again. Explicit native credentials still take precedence.
	if bundled && tdlHasSession(dataDir) {
		defaults = APIConfig{}
	}
	native, err := newTelegram(dataDir, defaults)
	if err != nil || !bundled || native.Status().State != "setup" {
		return native, err
	}
	native.Close()
	return newTDLProvider(dataDir, tdlBinary)
}

func newTelegram(dataDir string, defaults APIConfig) (Telegram, error) {
	dir := filepath.Join(dataDir, "telegram")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, telegramFailure("disk_write_failed")
	}

	provider := &telegramProvider{
		account: Account{State: "setup"},
		apiPath: filepath.Join(dir, "api.json"),
		storage: &telegramStorage{path: filepath.Join(dir, "session.json")},
	}
	data, err := os.ReadFile(provider.apiPath)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if !validTelegramConfig(defaults) {
			return provider, nil
		}
		provider.config = defaults
		provider.preconfigured = true
		// Keep release sessions tied to the exact API application that authorized them.
		provider.storage.path = filepath.Join(dir, "session-app-"+strconv.Itoa(defaults.APIID)+".json")
	case err != nil:
		return nil, telegramFailure("disk_write_failed")
	default:
		if err := json.Unmarshal(data, &provider.config); err != nil || !validTelegramConfig(provider.config) {
			provider.config = APIConfig{}
			provider.account.Error = telegramFailure("config_invalid")
			return provider, nil
		}
	}

	provider.account.State = "disconnected"
	if provider.storage.exists() {
		provider.start(true)
	}
	return provider, nil
}

func validTelegramConfig(config APIConfig) bool {
	if config.APIID <= 0 || config.APIID > 2147483647 || len(config.APIHash) != 32 {
		return false
	}
	_, err := hex.DecodeString(config.APIHash)
	return err == nil
}

func (p *telegramProvider) Status() Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	account := p.account
	account.Preconfigured = p.preconfigured
	return account
}

func (p *telegramProvider) Configure(config APIConfig) error {
	config.APIHash = strings.ToLower(strings.TrimSpace(config.APIHash))
	if !validTelegramConfig(config) {
		return telegramFailure("config_invalid")
	}

	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()

	p.mu.Lock()
	sameConfig := p.config == config
	active := p.run != nil
	p.mu.Unlock()
	if sameConfig {
		return nil
	}
	if active {
		return telegramFailure("login_in_progress")
	}

	// A different API application must never reuse an old authorization.
	storage := &telegramStorage{path: filepath.Join(filepath.Dir(p.apiPath), "session.json")}
	if err := storage.StoreSession(context.Background(), nil); err != nil {
		return telegramFailure("disk_write_failed")
	}
	if err := writePrivateFile(p.apiPath, func(w io.Writer) error {
		return json.NewEncoder(w).Encode(config)
	}); err != nil {
		return telegramFailure("disk_write_failed")
	}

	p.mu.Lock()
	p.config = config
	p.preconfigured = false
	p.storage = storage
	p.account = Account{State: "disconnected"}
	p.mu.Unlock()
	return nil
}

func (p *telegramProvider) StartLogin() error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()

	p.mu.Lock()
	configured := validTelegramConfig(p.config)
	active := p.run != nil
	p.mu.Unlock()
	if !configured {
		return telegramFailure("config_required")
	}
	if active {
		return telegramFailure("login_in_progress")
	}

	p.start(false)
	return nil
}

// start expects the caller to own the lifecycle lock, or to be initializing
// the provider before it is returned to the queue.
func (p *telegramProvider) start(restore bool) {
	ctx, cancel := context.WithCancelCause(context.Background())
	dispatch := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(&dispatch)
	run := &telegramRun{
		ctx:        ctx,
		cancel:     func() { cancel(nil) },
		done:       make(chan struct{}),
		passwords:  make(chan string, 1),
		hadSession: p.storage.exists(),
	}

	p.mu.Lock()
	config := p.config
	run.client = telegram.NewClient(config.APIID, config.APIHash, telegram.Options{
		SessionStorage: p.storage,
		UpdateHandler:  dispatch,
		Device: telegram.DeviceConfig{
			DeviceModel:   "TG NestFetch",
			SystemVersion: runtime.GOOS + "/" + runtime.GOARCH,
			AppVersion:    "0.1.3",
		},
		DialTimeout:  8 * time.Second,
		FilePoolSize: 2,
		AllowCDN:     true,
		OnAuthLost: func(_ context.Context, err error) error {
			return err
		},
	})
	p.run = run
	p.account = Account{State: "starting"}
	p.mu.Unlock()

	go p.runClient(run, loggedIn, restore, cancel)
}

func (p *telegramProvider) runClient(
	run *telegramRun, loggedIn qrlogin.LoggedIn, restore bool, cancel context.CancelCauseFunc,
) {
	defer close(run.done)
	defer run.cancel()
	connecting := time.AfterFunc(connectDeadline, func() { cancel(context.DeadlineExceeded) })
	defer connecting.Stop()

	err := run.client.Run(run.ctx, func(ctx context.Context) error {
		connecting.Stop()
		user, err := p.authorize(ctx, run, loggedIn, restore)
		if err != nil {
			return err
		}
		p.setAccount(run, Account{State: "connected", Label: telegramUserLabel(user)})
		<-ctx.Done()
		return ctx.Err()
	})
	if errors.Is(context.Cause(run.ctx), context.DeadlineExceeded) {
		err = context.DeadlineExceeded
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.run != run {
		return
	}
	p.run = nil
	switch {
	case errors.Is(err, errNoTelegramSession):
		p.account = Account{State: "disconnected"}
	case telegram.IsAuthLost(err):
		p.account = Account{State: "disconnected", Error: telegramFailure("session_expired")}
	case errors.Is(err, context.Canceled), err == nil:
		p.account = Account{State: "disconnected"}
	default:
		failure := mapTelegramFailure(err)
		state := "error"
		if failure.Code == "session_expired" {
			state = "disconnected"
		}
		p.account = Account{State: state, Error: failure}
	}
}

func (p *telegramProvider) authorize(ctx context.Context, run *telegramRun, loggedIn qrlogin.LoggedIn, restore bool) (*tg.User, error) {
	if restore || run.hadSession {
		check, cancel := context.WithTimeout(ctx, connectDeadline)
		status, err := run.client.Auth().Status(check)
		cancel()
		if err != nil {
			return nil, err
		}
		if status.Authorized {
			return status.User, nil
		}
		if restore {
			return nil, errNoTelegramSession
		}
	}

	login, cancel := context.WithTimeout(ctx, loginDeadline)
	defer cancel()
	ready, initial := context.WithCancelCause(login)
	defer initial(nil)
	timer := time.AfterFunc(connectDeadline, func() { initial(context.DeadlineExceeded) })
	defer timer.Stop()
	authorization, err := run.client.QR().Auth(ready, loggedIn, func(_ context.Context, token qrlogin.Token) error {
		timer.Stop()
		p.setAccount(run, Account{State: "qr", QRURL: token.URL(), Expires: token.Expires().Unix()})
		return nil
	})
	if errors.Is(context.Cause(ready), context.DeadlineExceeded) {
		return nil, context.DeadlineExceeded
	}
	if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") {
		authorization, err = p.waitForPassword(login, run)
	}
	if err != nil {
		return nil, err
	}
	user, ok := authorization.User.(*tg.User)
	if !ok {
		return nil, telegramFailure("telegram_error")
	}
	return user, nil
}

func (p *telegramProvider) setAccount(run *telegramRun, account Account) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.run == run {
		p.account = account
	}
}

func (p *telegramProvider) waitForPassword(ctx context.Context, run *telegramRun) (*tg.AuthAuthorization, error) {
	var previous *Failure
	for {
		p.mu.Lock()
		if p.run != run {
			p.mu.Unlock()
			return nil, context.Canceled
		}
		run.accepting = true
		p.account = Account{State: "password", Error: previous}
		p.mu.Unlock()

		var password string
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case password = <-run.passwords:
		}

		check, cancel := context.WithTimeout(ctx, connectDeadline)
		result, err := run.client.Auth().Password(check, password)
		cancel()
		if !errors.Is(err, auth.ErrPasswordInvalid) {
			return result, err
		}
		previous = telegramFailure("password_invalid")
	}
}

func (p *telegramProvider) SubmitPassword(password string) error {
	if password == "" || len(password) > 4096 {
		return telegramFailure("password_invalid")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	run := p.run
	if run == nil || !run.accepting {
		return telegramFailure("login_not_pending")
	}
	run.accepting = false
	p.account.Error = nil
	run.passwords <- password
	return nil
}

func (p *telegramProvider) CancelLogin() error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()

	p.mu.Lock()
	connected := p.account.State == "connected"
	run := p.run
	if connected || run == nil {
		p.mu.Unlock()
		return telegramFailure("login_not_pending")
	}
	// Validation can be canceled while the stored authorization is still
	// valid. Keep that authorization for the next attempt.
	preserveSession := run.hadSession && p.account.State == "starting"
	p.run = nil
	p.account = Account{State: "disconnected"}
	p.mu.Unlock()
	stopTelegramRun(run)
	if preserveSession {
		return nil
	}
	if err := p.storage.StoreSession(context.Background(), nil); err != nil {
		return telegramFailure("disk_write_failed")
	}
	return nil
}

func (p *telegramProvider) SignOut(ctx context.Context) error {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()

	p.mu.Lock()
	run := p.run
	connected := p.account.State == "connected"
	configured := validTelegramConfig(p.config)
	p.run = nil
	if configured {
		p.account = Account{State: "disconnected"}
	}
	p.mu.Unlock()

	var remoteError error
	if run != nil && connected {
		bounded, cancel := context.WithTimeout(ctx, 12*time.Second)
		_, remoteError = run.client.LogOut(bounded)
		cancel()
	}
	stopTelegramRun(run)
	if err := p.storage.StoreSession(context.Background(), nil); err != nil {
		return telegramFailure("disk_write_failed")
	}
	if remoteError != nil {
		return mapTelegramFailure(remoteError)
	}
	return nil
}

func (p *telegramProvider) Close() {
	p.lifecycle.Lock()
	defer p.lifecycle.Unlock()
	p.mu.Lock()
	run := p.run
	p.run = nil
	p.mu.Unlock()
	stopTelegramRun(run)
}

func stopTelegramRun(run *telegramRun) {
	if run != nil {
		run.cancel()
		<-run.done
	}
}

func (p *telegramProvider) sessionFor(parent context.Context) (*telegram.Client, context.Context, func(), error) {
	p.mu.Lock()
	run := p.run
	connected := p.account.State == "connected"
	p.mu.Unlock()
	if run == nil || !connected {
		return nil, nil, nil, telegramFailure("login_required")
	}
	ctx, cancel := context.WithCancel(parent)
	stop := context.AfterFunc(run.ctx, cancel)
	return run.client, ctx, func() {
		stop()
		cancel()
	}, nil
}

func telegramUserLabel(user *tg.User) string {
	if user == nil {
		return ""
	}
	if user.Username != "" {
		return "@" + user.Username
	}
	return telegramDisplayText(user.FirstName + " " + user.LastName)
}

func telegramFailure(code string) *Failure { return &Failure{Code: code} }

func mapTelegramFailure(err error) *Failure {
	var failure *Failure
	switch {
	case errors.As(err, &failure):
		return failure
	case errors.Is(err, auth.ErrPasswordInvalid):
		return telegramFailure("password_invalid")
	case tgerr.Is(err, "API_ID_INVALID", "API_ID_PUBLISHED_FLOOD", "API_HASH_INVALID"):
		return telegramFailure("telegram_credentials_invalid")
	case telegram.IsAuthLost(err), auth.IsUnauthorized(err):
		return telegramFailure("session_expired")
	case tgerr.Is(err, "CHANNEL_PRIVATE", "CHANNEL_INVALID", "CHAT_ADMIN_REQUIRED", "PEER_ID_INVALID", "USER_BANNED_IN_CHANNEL"):
		return telegramFailure("access_denied")
	case tgerr.Is(err, "MESSAGE_ID_INVALID", "USERNAME_NOT_OCCUPIED", "USERNAME_INVALID"):
		return telegramFailure("message_not_found")
	case tgerr.Is(err, "CHAT_FORWARDS_RESTRICTED"):
		return telegramFailure("protected_media")
	}
	if _, ok := tgerr.AsFloodWait(err); ok {
		return telegramFailure("telegram_rate_limited")
	}
	var networkError net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &networkError) {
		return telegramFailure("telegram_network")
	}
	return telegramFailure("telegram_error")
}

type telegramStorage struct {
	mu   sync.Mutex
	path string
}

func (s *telegramStorage) exists() bool {
	info, err := os.Stat(s.path)
	return err == nil && info.Size() > 0
}

func (s *telegramStorage) LoadSession(context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, session.ErrNotFound
	}
	return data, err
}

func (s *telegramStorage) StoreSession(_ context.Context, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(data) == 0 {
		err := os.Remove(s.path)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return err
	}
	return writePrivateFile(s.path, func(w io.Writer) error {
		_, err := w.Write(data)
		return err
	})
}
