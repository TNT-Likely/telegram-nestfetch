// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

func main() {
	if err := runEngine(); err != nil {
		fmt.Fprintln(os.Stderr, "TG NestFetch:", err)
		os.Exit(1)
	}
}

func runEngine() error {
	dataDir := flag.String("data-dir", "", "application data directory")
	downloadDir := flag.String("download-dir", "", "initial download location")
	frameHelper := flag.String("frame-helper", "", "optional local video thumbnail helper")
	flag.Parse()

	if !filepath.IsAbs(*dataDir) || !filepath.IsAbs(*downloadDir) {
		return errors.New("data-dir and download-dir require absolute paths")
	}
	if err := os.MkdirAll(*dataDir, 0700); err != nil {
		return err
	}
	lock, err := lockDataDirectory(*dataDir)
	if err != nil {
		return err
	}
	defer lock.Close()

	telegram, err := newTelegram(*dataDir)
	if err != nil {
		return err
	}
	defer telegram.Close()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	q, err := openQueue(ctx, *dataDir, *downloadDir, *frameHelper, telegram)
	if err != nil {
		return err
	}
	defer q.close()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, os.Stdin, os.Stdout, q) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = os.Stdin.Close()
		// Give an IPC request a moment to observe shutdown without depending
		// on the parent to close its pipe.
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, os.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) {
				return err
			}
		case <-time.After(time.Second):
		}
		return nil
	}
}
