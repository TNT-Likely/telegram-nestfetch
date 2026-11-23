// SPDX-License-Identifier: MIT
package main

import (
	"bytes"
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/TNT-Likely/telegram-nestfetch/engine/preview"
)

const tdlPreviewPrefix = "NESTFETCH_PREVIEW "

// Preview returns after the playback session is closed and its tdl process
// has released the desktop account's storage.
func (p *tdlProvider) Preview(parent context.Context, link MessageLink, ready func(string)) error {
	if err := p.requireTDLSession(); err != nil {
		return err
	}
	lookup, stop := context.WithTimeout(parent, 35*time.Second)
	record, err := p.readTDLMedia(lookup, link, true)
	timedOut := lookup.Err() == context.DeadlineExceeded
	stop()
	if timedOut {
		return telegramFailure("telegram_network")
	}
	if err != nil {
		return err
	}
	if record.media.Kind != MediaVideo {
		return telegramFailure("preview_video_required")
	}
	if record.media.HideThumbnail {
		return telegramFailure("protected_media")
	}

	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cmd := p.tdlCommand(ctx, "nestfetch-preview", "--",
		strconv.FormatInt(record.channelID, 10), strconv.Itoa(record.messageID),
		strconv.FormatInt(record.media.ID, 10), strconv.FormatInt(record.media.Bytes, 10))
	input, err := cmd.StdinPipe()
	if err != nil {
		return telegramFailure("preview_unavailable")
	}
	defer input.Close()
	output := tdlPreviewOutput{opened: make(chan string, 1)}
	var errorsOutput tdlMediaOutput
	cmd.Stdout, cmd.Stderr = &output, &errorsOutput
	if err := cmd.Start(); err != nil {
		return p.tdlSessionError(ctx, err, "")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case address := <-output.opened:
		if ctx.Err() == nil {
			ready(address)
		}
	case err := <-done:
		return p.previewProcessFailure(ctx, err, string(output.log)+"\n"+string(errorsOutput))
	case <-timer.C:
		cancel()
		<-done
		return telegramFailure("telegram_network")
	case <-ctx.Done():
		<-done
		return ctx.Err()
	}
	err = <-done
	return p.previewProcessFailure(ctx, err, string(output.log)+"\n"+string(errorsOutput))
}

func (p *tdlProvider) previewProcessFailure(ctx context.Context, runErr error, log string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	err := p.tdlSessionError(ctx, runErr, log)
	if err != nil && transferFailure(err).Code != "telegram_error" {
		return err
	}
	if strings.Contains(log, "NESTFETCH_PREVIEW_MEDIA_CHANGED") {
		return telegramFailure("media_changed")
	}
	return telegramFailure("preview_unavailable")
}

// The readiness line is read from the process pipe, separate from later
// error mapping. Only the expected loopback URL is sent to the UI.
type tdlPreviewOutput struct {
	opened  chan string
	log     tdlMediaOutput
	pending []byte
	discard bool
}

func (o *tdlPreviewOutput) Write(data []byte) (int, error) {
	written := len(data)
	for len(data) > 0 {
		line, rest, complete := bytes.Cut(data, []byte{'\n'})
		data = rest
		if len(line) > 256-len(o.pending) {
			o.discard = true
		}
		if !o.discard {
			o.pending = append(o.pending, line...)
		}
		if !complete {
			break
		}
		if !o.discard {
			if address, ok := strings.CutPrefix(string(bytes.TrimRight(o.pending, "\r")), tdlPreviewPrefix); ok {
				if preview.ValidURL(address) {
					select {
					case o.opened <- address:
					default:
					}
				}
			} else {
				_, _ = o.log.Write(o.pending)
				_, _ = o.log.Write([]byte{'\n'})
			}
		}
		o.pending, o.discard = o.pending[:0], false
	}
	return written, nil
}
