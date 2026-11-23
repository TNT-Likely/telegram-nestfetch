// SPDX-License-Identifier: MIT
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const fixturePassword = "  密码 fixture  "

func TestTDLPasswordThroughPipe(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "-test.run=^TestTDLPasswordPipeFixture$")
	cmd.Env = append(os.Environ(), "NESTFETCH_PASSWORD_PIPE_FIXTURE=1")
	tdlConfigureProcess(cmd)
	terminal, err := startTDLTerminal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer terminal.output.Close()
	defer func() {
		if cmd.ProcessState == nil {
			cancel()
			_ = cmd.Wait()
		}
	}()

	login := &tdlLogin{terminal: terminal, cancel: cancel}
	provider := &tdlProvider{login: login, account: tdlAccount("starting")}
	result := readTDLLoginOutput(terminal.output, func(event tdlLoginEvent) {
		provider.updateTDLLogin(login, event)
		if !event.password {
			return
		}
		if state := provider.Status().State; state != "password" {
			t.Fatalf("password prompt left the account in state %q", state)
		}
		if err := provider.SubmitPassword(fixturePassword); err != nil {
			t.Fatal(err)
		}
	})
	if err := cmd.Wait(); err != nil {
		t.Fatalf("tdl pipe fixture exited: %v (%s)", err, result.output)
	}
	if !result.accepted || strings.Contains(result.output, fixturePassword) {
		t.Fatal("tdl did not accept the password through its private pipe")
	}
}

func TestTDLPasswordPipeFixture(t *testing.T) {
	if os.Getenv("NESTFETCH_PASSWORD_PIPE_FIXTURE") != "1" {
		return
	}
	if os.Getenv("TDL_NESTFETCH_PASSWORD_STDIN") != "1" {
		t.Fatal("tdl did not receive its stdin password option")
	}
	fmt.Println("Enter 2FA Password:")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil || line != fixturePassword+"\n" {
		t.Fatal("password was not delivered intact")
	}
	fmt.Println("Login successfully! ID: 101, Username: test_login")
}
