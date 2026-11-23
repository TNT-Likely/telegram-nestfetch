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

const (
	tdlPreviewPrefix     = "NESTFETCH_PREVIEW "
	tdlPreviewReadPrefix = "NESTFETCH_PREVIEW_READ "
	maxTDLPreviewRead    = 30 * time.Second
)

// Preview returns after the playback session is closed and its tdl process
// has released the desktop account's storage.
func (p *tdlProvider) Preview(parent context.Context, link MessageLink,
	ready func(string), report func(int64, time.Duration),
) error {
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
	output := tdlPreviewOutput{
		opened: make(chan string, 1),
		report: func(count int64, elapsed time.Duration) {
			if report != nil && ctx.Err() == nil {
				report(count, elapsed)
			}
		},
	}
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

// Readiness and transfer measurements are consumed separately from diagnostics.
type tdlPreviewOutput struct {
	opened    chan string
	report    func(int64, time.Duration)
	announced bool
	log       tdlMediaOutput
	pending   []byte
	discard   bool
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
			o.consumeLine(string(bytes.TrimRight(o.pending, "\r")))
		}
		o.pending, o.discard = o.pending[:0], false
	}
	return written, nil
}

func (o *tdlPreviewOutput) consumeLine(line string) {
	if address, ok := strings.CutPrefix(line, tdlPreviewPrefix); ok {
		if !o.announced && preview.ValidURL(address) {
			select {
			case o.opened <- address:
				o.announced = true
			default:
			}
		}
		return
	}
	if measurement, ok := strings.CutPrefix(line, tdlPreviewReadPrefix); ok {
		if !o.announced || o.report == nil {
			return
		}
		countText, elapsedText, ok := strings.Cut(measurement, " ")
		if !ok {
			return
		}
		count, countErr := strconv.ParseUint(countText, 10, 32)
		nanos, elapsedErr := strconv.ParseUint(elapsedText, 10, 64)
		if countErr == nil && elapsedErr == nil && count > 0 && count <= preview.BlockSize &&
			nanos > 0 && nanos <= uint64(maxTDLPreviewRead) {
			o.report(int64(count), time.Duration(nanos))
		}
		return
	}
	_, _ = o.log.Write([]byte(line))
	_, _ = o.log.Write([]byte{'\n'})
}
