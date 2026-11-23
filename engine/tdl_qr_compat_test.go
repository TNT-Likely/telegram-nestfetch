// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/neo"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgmock"
)

const tdlTestAPIID = 490001
const tdlTestAPIHash = "compatibility-test-only"

func tdlTokenRequest() *tg.AuthExportLoginTokenRequest {
	return &tg.AuthExportLoginTokenRequest{APIID: tdlTestAPIID, APIHash: tdlTestAPIHash}
}

func TestTDLQRHandlesMigrationDuringRefresh(t *testing.T) {
	testClock := neo.NewTime(time.Unix(1700000000, 0))
	server := tgmock.New(t)
	var migrated int
	client := qrlogin.NewQR(tg.NewClient(server), tdlTestAPIID, tdlTestAPIHash, qrlogin.Options{
		Clock: testClock,
		Migrate: func(_ context.Context, dc int) error {
			migrated = dc
			return nil
		},
	})
	server.ExpectCall(tdlTokenRequest()).ThenResult(&tg.AuthLoginToken{
		Expires: int(testClock.Now().Add(30 * time.Second).Unix()),
		Token:   []byte("synthetic initial token"),
	}).
		ExpectCall(tdlTokenRequest()).ThenResult(&tg.AuthLoginTokenMigrateTo{
		DCID: 4, Token: []byte("synthetic migration token"),
	}).
		ExpectCall(&tg.AuthImportLoginTokenRequest{Token: []byte("synthetic migration token")}).
		ThenResult(&tg.AuthLoginTokenSuccess{Authorization: &tg.AuthAuthorization{User: &tg.User{ID: 101}}})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	auth, err := client.Auth(ctx, make(chan struct{}), func(context.Context, qrlogin.Token) error {
		testClock.Travel(31 * time.Second)
		return nil
	})
	var migrationErr *qrlogin.MigrationNeededError
	switch {
	case errors.As(err, &migrationErr):
		t.Fatalf("client returned migration-needed on refresh (DC %d) without importing", migrationErr.MigrateTo.DCID)
	case err != nil:
		t.Fatal(err)
	case migrated != 4 || auth.User.(*tg.User).ID != 101:
		t.Fatalf("wrong migration or authorization")
	}
}

type loginDispatch struct{ update tg.LoginTokenHandler }

func (d *loginDispatch) OnLoginToken(handler tg.LoginTokenHandler) { d.update = handler }

func TestTDLQRKeepsConfirmationDuringDisplay(t *testing.T) {
	server := tgmock.New(t)
	testClock := neo.NewTime(time.Unix(1700000000, 0))
	client := qrlogin.NewQR(tg.NewClient(server), tdlTestAPIID, tdlTestAPIHash, qrlogin.Options{Clock: testClock})
	dispatch := new(loginDispatch)
	login := qrlogin.OnLoginToken(dispatch)
	server.ExpectCall(tdlTokenRequest()).ThenResult(&tg.AuthLoginToken{
		Expires: int(testClock.Now().Add(30 * time.Second).Unix()), Token: []byte("synthetic token"),
	}).
		ExpectCall(tdlTokenRequest()).ThenResult(&tg.AuthLoginTokenSuccess{
		Authorization: &tg.AuthAuthorization{User: &tg.User{ID: 101}},
	})
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	auth, err := client.Auth(ctx, login, func(ctx context.Context, _ qrlogin.Token) error {
		return dispatch.update(ctx, tg.Entities{}, &tg.UpdateLoginToken{})
	})
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("client dropped the login signal issued while it was displaying the QR")
	}
	if err != nil {
		t.Fatal(err)
	}
	if auth.User.(*tg.User).ID != 101 {
		t.Fatal("wrong synthetic user")
	}
}
