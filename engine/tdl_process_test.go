// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTDLCommandFailuresUseStableRedactedCodes(t *testing.T) {
	cases := []struct {
		message string
		code    string
	}{
		{"Error: API_ID_INVALID", "tdl_credentials_rejected"},
		{"Error: API_ID_PUBLISHED_FLOOD", "tdl_credentials_rejected"},
		{"Error: API_HASH_INVALID", "tdl_credentials_rejected"},
		{"Error: session_revoked", "session_expired"},
		{"Error: PASSWORD_HASH_INVALID", "password_invalid"},
	}
	for _, test := range cases {
		err := tdlCommandFailure(context.Background(), nil, test.message+" /secret/fixture.mp4")
		var failure *Failure
		if !errors.As(err, &failure) || failure.Code != test.code || failure.Message != "" {
			t.Errorf("failure code = %v, want %s", err, test.code)
		}
	}
	if err := tdlCommandFailure(context.Background(), nil, "file not authorized.mp4"); err != nil {
		t.Fatal("classified successful output as an authentication error")
	}
}

func TestLoginRPCSurvivesLongTerminalTrace(t *testing.T) {
	trace := "Error: import: rpc error code 400: AUTH_TOKEN_EXPIRED\n" +
		strings.Repeat("frame /private/example with other details\n", 180)
	result := readTDLLoginOutput(strings.NewReader(trace), func(tdlLoginEvent) {})
	if result.issue == nil || result.issue.Code != "tdl_login_token_invalid" || result.issue.Message != "AUTH_TOKEN_EXPIRED" {
		t.Fatal("login did not preserve the Telegram error identifier")
	}
	migration := tdlLoginIssue("Error: qr auth: migration to 4 needed")
	if migration == nil || migration.Code != "tdl_login_migration" {
		t.Fatal("login migration failure was not recognized")
	}
}
