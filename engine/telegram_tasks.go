// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"time"
)

const browseTimeout = 40 * time.Second

type telegramPreviewer interface {
	Preview(context.Context, MessageLink, func(string)) error
}

type telegramTaskStatus struct {
	State      string              `json:"state"`
	Waiting    bool                `json:"waiting,omitempty"`
	Channels   []channelSummary    `json:"channels,omitempty"`
	Items      []channelAttachment `json:"items,omitempty"`
	NextBefore int                 `json:"nextBefore,omitempty"`
	URL        string              `json:"url,omitempty"`
	Error      *Failure            `json:"error,omitempty"`
}

type telegramTaskRun func(context.Context, func(telegramTaskStatus)) (telegramTaskStatus, error)

// Background reads and previews share the same scheduler as downloads.
// The bundled tdl uses an exclusive session database per process.
type telegramTask struct {
	id     string
	mode   string
	ctx    context.Context
	cancel context.CancelFunc
	status telegramTaskStatus
	run    telegramTaskRun
	begun  bool
}

func (q *queue) startChannelList() (string, error) {
	provider, ok := q.telegram.(channelBrowser)
	if !ok {
		return "", telegramFailure("browse_unavailable")
	}
	return q.startTelegramTask("channels", func(ctx context.Context, _ func(telegramTaskStatus)) (telegramTaskStatus, error) {
		read, cancel := context.WithTimeout(ctx, browseTimeout)
		defer cancel()
		channels, err := provider.ListChannels(read)
		if read.Err() == context.DeadlineExceeded {
			err = telegramFailure("telegram_network")
		}
		return telegramTaskStatus{Channels: channels}, err
	})
}

func (q *queue) startAttachmentList(filter channelQuery) (string, error) {
	if err := filter.validate(); err != nil {
		return "", err
	}
	provider, ok := q.telegram.(channelBrowser)
	if !ok {
		return "", telegramFailure("browse_unavailable")
	}
	return q.startTelegramTask("attachments", func(ctx context.Context, _ func(telegramTaskStatus)) (telegramTaskStatus, error) {
		read, cancel := context.WithTimeout(ctx, browseTimeout)
		defer cancel()
		page, err := provider.ListAttachments(read, filter)
		if read.Err() == context.DeadlineExceeded {
			err = telegramFailure("telegram_network")
		}
		return telegramTaskStatus{Items: page.Items, NextBefore: page.NextBefore}, err
	})
}

func (q *queue) startPreview(rawURL string) (string, error) {
	link, err := parseMessageLink(rawURL)
	if err != nil {
		return "", err
	}
	provider, ok := q.telegram.(telegramPreviewer)
	if !ok {
		return "", telegramFailure("preview_unavailable")
	}
	return q.startTelegramTask("preview", func(ctx context.Context, update func(telegramTaskStatus)) (telegramTaskStatus, error) {
		err := provider.Preview(ctx, link, func(address string) {
			update(telegramTaskStatus{State: "ready", URL: address})
		})
		if err == nil {
			err = telegramFailure("preview_unavailable")
		}
		return telegramTaskStatus{}, err
	})
}

func (q *queue) startTelegramTask(mode string, run telegramTaskRun) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", localFailure(err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.telegram.Status().State != "connected" {
		return "", telegramFailure("login_required")
	}
	if q.telegramTask != nil {
		q.telegramTask.cancel()
	}
	ctx, cancel := context.WithCancel(q.ctx)
	q.telegramTask = &telegramTask{
		id: id, mode: mode, ctx: ctx, cancel: cancel, run: run,
		status: telegramTaskStatus{State: "loading"},
	}
	q.notify()
	return id, nil
}

func (q *queue) telegramTaskStatus(mode, id string) (telegramTaskStatus, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	task := q.telegramTask
	if task == nil || task.id != id || task.mode != mode {
		return telegramTaskStatus{}, telegramFailure("not_found")
	}
	status := task.status
	status.Waiting = status.State == "loading" && !task.begun && q.activeID != ""
	return status, nil
}

func (q *queue) cancelTelegramTask(id string, preview bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	task := q.telegramTask
	if task != nil && task.id == id && (task.mode == "preview") == preview {
		task.cancel()
		q.telegramTask = nil
		q.notify()
	}
}

func (q *queue) runTelegramTask(task *telegramTask) {
	defer task.cancel()
	update := func(status telegramTaskStatus) {
		q.mu.Lock()
		defer q.mu.Unlock()
		if q.telegramTask == task && task.ctx.Err() == nil {
			task.status = status
		}
	}
	result, err := task.run(task.ctx, update)
	if err != nil {
		update(telegramTaskStatus{State: "failed", Error: transferFailure(err)})
	} else {
		result.State = "ready"
		update(result)
	}
	q.mu.Lock()
	q.accountBusy = false
	q.mu.Unlock()
}
