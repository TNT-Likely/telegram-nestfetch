// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"
)

const tdlNamespace = "nestfetch"

var tdlRPCCode = regexp.MustCompile(`\b(?i:rpc error code) [0-9]{3}: ([A-Z][A-Z0-9_]{2,63})\b`)

// Save only a protocol error identifier or a known stage. A later stack trace
// can push the original error out of the bounded terminal buffer.
func tdlLoginIssue(line string) *Failure {
	lower := strings.ToLower(line)
	if match := tdlRPCCode.FindStringSubmatch(line); match != nil {
		failure := transferFailure(tdlCommandFailure(context.Background(), errors.New("tdl login"), line))
		failure.Message = match[1]
		return failure
	}
	switch {
	case strings.Contains(lower, "migration to ") && strings.Contains(lower, " needed"):
		return &Failure{Code: "tdl_login_migration", Message: "DC_MIGRATION"}
	case strings.Contains(lower, "unexpected type *tg.authlogintoken"):
		return &Failure{Code: "tdl_login_response", Message: "UNEXPECTED_LOGIN_TOKEN"}
	case strings.Contains(lower, "unexpected constructor") || strings.Contains(lower, "unknown constructor"):
		return &Failure{Code: "tdl_login_response", Message: "UNKNOWN_CONSTRUCTOR"}
	}
	return nil
}

// tdlCommand only starts the separately installed or bundled upstream
// executable with an app-owned home and namespace. The relative storage path
// avoids passing user paths through tdl's CSV parser.
func (p *tdlProvider) tdlCommand(ctx context.Context, args ...string) *exec.Cmd {
	flags := []string{
		"--ns", tdlNamespace, "--storage", "type=bolt,path=data",
		"--reconnect-timeout", "25s", "--limit", "1", "--threads", "1", "--pool", "2",
	}
	cmd := exec.CommandContext(ctx, p.binary, append(flags, args...)...)
	cmd.Dir = p.dir
	cmd.Env = tdlEnvironment(os.Environ(), p.home)
	if p.proxy != "" {
		cmd.Env = append(cmd.Env, "TDL_PROXY="+p.proxy)
	}
	cmd.Cancel = func() error {
		if runtime.GOOS == "windows" {
			return cmd.Process.Kill()
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = 1800 * time.Millisecond
	tdlConfigureProcess(cmd)
	return cmd
}

func tdlEnvironment(inherited []string, home string) []string {
	env := make([]string, 0, len(inherited)+4)
	for _, entry := range inherited {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		switch key {
		case "HOME", "USERPROFILE", "NO_COLOR", "TERM":
			continue
		}
		if !strings.HasPrefix(key, "TDL_") {
			env = append(env, entry)
		}
	}
	return append(env, "HOME="+home, "USERPROFILE="+home, "NO_COLOR=1", "TERM=xterm")
}

func tdlCommandFailure(ctx context.Context, runErr error, output string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if errors.Is(runErr, fs.ErrNotExist) || errors.Is(runErr, exec.ErrNotFound) {
		return telegramFailure("tdl_unavailable")
	}
	// Do not propagate the process's output: errors can contain chat URLs,
	// file paths or terminal output from login.
	lower := strings.ToLower(output)
	if runErr == nil && !strings.Contains(lower, "error:") {
		return nil
	}
	switch {
	case tdlIncludes(lower, "auth_token_expired", "auth_token_invalid", "auth_token_already_accepted"):
		return telegramFailure("tdl_login_token_invalid")
	case tdlIncludes(lower, "update_app_to_login"):
		return telegramFailure("tdl_login_response")
	case tdlIncludes(lower, "password_hash_invalid", "password is invalid", "invalid password", "password invalid"):
		return telegramFailure("password_invalid")
	case tdlIncludes(lower, "api_id_invalid", "api_id_published_flood", "api_hash_invalid"):
		return telegramFailure("tdl_credentials_rejected")
	case tdlIncludes(lower, "not authorized", "not logged in", "please login first", "session not found", "no session found",
		"session_revoked", "session_expired", "auth_key_unregistered", "auth_key_duplicated", "auth_key_invalid"):
		return telegramFailure("session_expired")
	case tdlIncludes(lower, "channel_private", "channel_invalid", "peer_id_invalid", "username_not_occupied",
		"chat_admin_required", "peer unresolvable", "can't find peer", "cannot resolve peer", "no chat found"):
		return telegramFailure("access_denied")
	case tdlIncludes(lower, "chat_forwards_restricted"):
		return telegramFailure("protected_media")
	case tdlIncludes(lower, "message_id_invalid"):
		return telegramFailure("message_not_found")
	case tdlIncludes(lower, "flood_wait", "flood_premium_wait"):
		return telegramFailure("telegram_rate_limited")
	case tdlIncludes(lower, "no space left", "permission denied", "database is used by another process"):
		return telegramFailure("disk_write_failed")
	case tdlIncludes(lower, "connection refused", "connection reset", "network is unreachable", "context deadline exceeded",
		"i/o timeout", "dial tcp", "handshake timeout", "reconnect timeout"):
		return telegramFailure("telegram_network")
	}
	if runErr != nil {
		return telegramFailure("telegram_error")
	}
	return nil
}

func tdlIncludes(text string, phrases ...string) bool {
	for _, phrase := range phrases {
		if strings.Contains(text, phrase) {
			return true
		}
	}
	return false
}

// Only transport failures observed after a child process has exited can
// invalidate the current local account marker.
func (p *tdlProvider) tdlSessionError(ctx context.Context, runErr error, output string) error {
	err := tdlCommandFailure(ctx, runErr, output)
	var failure *Failure
	if errors.As(err, &failure) && failure.Code == "session_expired" {
		p.lifecycle.Lock()
		defer p.lifecycle.Unlock()
		p.mu.Lock()
		if p.login == nil && p.account.State == "connected" {
			p.account = tdlAccount("disconnected")
			p.account.Error = failure
			p.mu.Unlock()
			_ = p.clearTDLStorage()
			return err
		}
		p.mu.Unlock()
	}
	return err
}
