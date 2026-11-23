//go:build darwin || linux

// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/AlecAivazis/survey/v2"
)

func TestTDLAccountIsolatesTerminalAndPersistsVerifiedLogin(t *testing.T) {
	temp := t.TempDir()
	fakeBinary := filepath.Join(temp, "tdl-fixture")
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executable := "'" + strings.ReplaceAll(testBinary, "'", "'\"'\"'") + "'"
	script := "#!/bin/sh\nexec " + executable + " -test.run='^TestTDLFixtureProcess$' -- \"$@\"\n"
	if err := os.WriteFile(fakeBinary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("NESTFETCH_TDL_FIXTURE", "1")
	t.Setenv("TDL_NS", "default")
	t.Setenv("TDL_STORAGE", "fixture-must-not-leak")
	p, err := newTDLProvider(temp, fakeBinary)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	first, _ := tdlFixtureFrame(t, "fixture: login video test")
	if err := os.WriteFile(filepath.Join(p.dir, "fixture-qr"), []byte(first), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.StartLogin(); err != nil {
		t.Fatal(err)
	}
	qr := waitTDLState(t, p, "qr")
	if qr.QRImage == "" || qr.QRURL != "" || qr.Backend != "tdl" || !qr.Preconfigured {
		t.Fatal("QR state missing PNG or provider metadata")
	}
	if tdlHasSession(temp) {
		t.Fatal("pending QR appeared as verified session")
	}
	if err := p.CancelLogin(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p.dbPath()); !os.IsNotExist(err) {
		t.Fatal("cancellation retained a pending session")
	}
	if err := p.StartLogin(); err != nil {
		t.Fatal(err)
	}
	waitTDLState(t, p, "qr")
	if err := os.WriteFile(filepath.Join(p.dir, "fixture-scan"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ask := waitTDLState(t, p, "password")
	if ask.QRImage != "" {
		t.Fatal("QR retained during password entry")
	}
	if err := p.SubmitPassword("密码 fixture"); err != nil {
		t.Fatal(err)
	}
	got := waitTDLState(t, p, "connected")
	if got.Label != "@test_login" || !tdlHasSession(temp) {
		t.Fatal("verified account not persisted")
	}
	info, err := os.Stat(p.dbPath())
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("fallback session is not private")
	}
	reloaded, err := newTDLProvider(temp, fakeBinary)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if reloaded.Status().State != "connected" || reloaded.Status().Label != "@test_login" {
		t.Fatal("verified session was lost across restart")
	}
	if err := reloaded.SignOut(context.Background()); err != nil {
		t.Fatal(err)
	}
	if tdlHasSession(temp) || reloaded.Status().State != "disconnected" {
		t.Fatal("disconnection retained the tdl session")
	}
}

// Run the same password prompt library and version as the pinned tdl
// executable, including both terminal cursor-position queries.
func TestTDLFixtureProcess(t *testing.T) {
	if os.Getenv("NESTFETCH_TDL_FIXTURE") != "1" {
		return
	}
	wantArgs := []string{"--ns", "nestfetch", "--storage", "type=bolt,path=data",
		"--reconnect-timeout", "25s", "--limit", "1", "--threads", "1", "--pool", "2", "login", "-T", "qr"}
	if args := os.Args[2:]; len(args) < 1 || args[0] != "--" || !slices.Equal(args[1:], wantArgs) {
		t.Fatal("incorrect tdl launch arguments")
	}
	pwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil || os.Getenv("USERPROFILE") != home ||
		os.Getenv("TDL_NS") != "" || os.Getenv("TDL_STORAGE") != "" {
		t.Fatal("tdl home or namespace is not isolated")
	}
	homeDir, homeErr := os.Stat(home)
	privateDir, privateErr := os.Stat(filepath.Join(pwd, "home"))
	if homeErr != nil || privateErr != nil || !os.SameFile(homeDir, privateDir) {
		t.Fatal("tdl uses a home outside the app's private directory")
	}
	frame, err := os.ReadFile("fixture-qr")
	if err != nil {
		t.Fatal(err)
	}
	fmt.Println("Scan QR code with your Telegram app...")
	_, _ = os.Stdout.Write(frame)
	for {
		if _, err := os.Stat("fixture-scan"); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	var password string
	if err := survey.AskOne(&survey.Password{Message: "Enter 2FA Password:"}, &password, survey.WithValidator(survey.Required)); err != nil {
		t.Fatal("password terminal did not receive its device status reports")
	}
	if password != "密码 fixture" {
		fmt.Println("Error: PASSWORD_HASH_INVALID")
		t.FailNow()
	}
	if err := os.WriteFile(filepath.Join("data", "nestfetch"), []byte("fixture session"), 0600); err != nil {
		t.Fatal(err)
	}
	fmt.Println("Login successfully! ID: 101, Username: test_login")
}

func waitTDLState(t *testing.T, p *tdlProvider, want string) Account {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		account := p.Status()
		if account.State == want {
			return account
		}
		if account.State == "error" || time.Now().After(deadline) {
			t.Fatalf("tdl account state = %s (%v), waiting for %s", account.State, account.Error, want)
		}
		time.Sleep(15 * time.Millisecond)
	}
}
