// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"errors"
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
