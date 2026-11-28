// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"math"
	"strings"
	"time"
)

const browseTimeout = 40 * time.Second
const previewSpeedIdle = 2 * time.Second

type telegramPreviewer interface {
	Preview(context.Context, MessageLink, func(string), func(int64, time.Duration)) error
}

type linkPreview struct {
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Name      string    `json:"name"`
	Source    string    `json:"source,omitempty"`
	Kind      MediaKind `json:"kind"`
	Size      int64     `json:"size"`
	Duration  float64   `json:"duration"`
	Thumbnail string    `json:"thumbnail,omitempty"`
	Duplicate bool      `json:"duplicate,omitempty"`
}

type taskStatus struct {
	State           string              `json:"state"`
	Waiting         bool                `json:"waiting,omitempty"`
	Channels        []channelSummary    `json:"channels,omitempty"`
	Items           []channelAttachment `json:"items,omitempty"`
	NextBefore      int                 `json:"nextBefore,omitempty"`
	Preview         *linkPreview        `json:"preview,omitempty"`
	URL             string              `json:"url,omitempty"`
	BytesRead       int64               `json:"bytesRead,omitempty"`
	Speed           int64               `json:"speed,omitempty"`
	Error           *Failure            `json:"error,omitempty"`
	lastPreviewRead time.Time
}

type updateTask func(func(*taskStatus))
type taskRun func(context.Context, updateTask) (taskStatus, error)

type mediaTask struct {
	id     string
	mode   string
	ctx    context.Context
	cancel context.CancelFunc
	status taskStatus
	run    taskRun
	begun  bool
}

func (q *queue) startChannelList() (string, error) {
	provider, ok := q.telegram.(channelBrowser)
	if !ok {
		return "", telegramFailure("browse_unavailable")
	}
	return q.startTelegramTask("channels", func(ctx context.Context, _ updateTask) (taskStatus, error) {
		read, cancel := context.WithTimeout(ctx, browseTimeout)
		defer cancel()
		channels, err := provider.ListChannels(read)
		if read.Err() == context.DeadlineExceeded {
			err = telegramFailure("telegram_network")
		}
		return taskStatus{Channels: channels}, err
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
	return q.startTelegramTask("attachments", func(ctx context.Context, _ updateTask) (taskStatus, error) {
		read, cancel := context.WithTimeout(ctx, browseTimeout)
		defer cancel()
		page, err := provider.ListAttachments(read, filter)
		if read.Err() == context.DeadlineExceeded {
			err = telegramFailure("telegram_network")
		}
		return taskStatus{Items: page.Items, NextBefore: page.NextBefore}, err
	})
}

func (q *queue) startInspect(rawURL string) (string, error) {
	link, err := parseMessageLink(rawURL)
	if err != nil {
		return "", err
	}
	return q.startTelegramTask("inspect", func(ctx context.Context, _ updateTask) (taskStatus, error) {
		read, cancel := context.WithTimeout(ctx, browseTimeout)
		defer cancel()
		media, err := q.telegram.Inspect(read, link)
		if read.Err() == context.DeadlineExceeded {
			err = telegramFailure("telegram_network")
		}
		if err != nil {
			return taskStatus{}, err
		}
		if media.ID == 0 || media.Bytes <= 0 {
			return taskStatus{}, &Failure{Code: "no_media"}
		}
		duration := media.Duration
		if math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0 {
			duration = 0
		}
		kind := media.Kind
		if kind == "" {
			kind = MediaFile
		}
		thumbnail := ""
		if !media.HideThumbnail && validThumbnail(media.Thumbnail) {
			thumbnail = media.Thumbnail
		}
		q.mu.Lock()
		duplicate := q.hasActiveOrSavedURLLocked(link.URL)
		q.mu.Unlock()
		return taskStatus{
			Preview: &linkPreview{
				URL:       link.URL,
				Title:     strings.TrimSpace(media.Title),
				Name:      filename(media.Name),
				Source:    media.Source,
				Kind:      kind,
				Size:      media.Bytes,
				Duration:  duration,
				Thumbnail: thumbnail,
				Duplicate: duplicate,
			},
		}, nil
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
	return q.startTelegramTask("preview", func(ctx context.Context, update updateTask) (taskStatus, error) {
		ready := func(address string) {
			update(func(status *taskStatus) { status.State, status.URL = "ready", address })
		}
		report := func(bytes int64, elapsed time.Duration) {
			update(func(status *taskStatus) { status.recordPreviewRead(bytes, elapsed, time.Now()) })
		}
		err := provider.Preview(ctx, link, ready, report)
		if err == nil {
			err = telegramFailure("preview_unavailable")
		}
		return taskStatus{}, err
	})
}

func (q *queue) startLocalPlayback(jobID string) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", localFailure(err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if err := q.ctx.Err(); err != nil {
		return "", err
	}
	if q.hold != queueRunning {
		return "", telegramFailure("busy")
	}
	entry := q.byIDLocked(jobID)
	if entry == nil {
		return "", telegramFailure("not_found")
	}
	if entry.State != completed || entry.Kind != MediaVideo {
		return "", telegramFailure("preview_video_required")
	}
	job := *entry
	if q.telegramTask != nil && q.telegramTask.mode == "preview" {
		q.telegramTask.cancel()
		q.telegramTask = nil
	}
	ctx, cancel := context.WithCancel(q.ctx)
	if q.localPlayback != nil {
		q.localPlayback.cancel()
	}
	task := &mediaTask{
		id: id, mode: "preview", ctx: ctx, cancel: cancel, begun: true,
		status: taskStatus{State: "loading"},
		run: func(ctx context.Context, update updateTask) (taskStatus, error) {
			err := playSavedVideo(ctx, job, func(address string) {
				update(func(status *taskStatus) { status.State, status.URL = "ready", address })
			})
			return taskStatus{}, err
		},
	}
	q.localPlayback = task
	// Local files use their own worker so playback remains available offline
	// and does not wait for the Telegram account or the download queue.
	q.workers.Go(func() { q.runMediaTask(task, &q.localPlayback) })
	return id, nil
}

func (s *taskStatus) recordPreviewRead(bytes int64, elapsed time.Duration, now time.Time) {
	if bytes <= 0 || elapsed <= 0 {
		return
	}
	rate := int64(float64(bytes) / max(elapsed.Seconds(), .001))
	s.BytesRead += bytes
	if s.Speed == 0 || now.Sub(s.lastPreviewRead) > previewSpeedIdle {
		s.Speed = rate
	} else {
		// Publish a smoothed rate so different-sized Telegram chunks don't
		// make the display jump on every update.
		s.Speed = (2*rate + 3*s.Speed) / 5
	}
	s.lastPreviewRead = now
}

func (q *queue) startTelegramTask(mode string, run taskRun) (string, error) {
	id, err := randomID()
	if err != nil {
		return "", localFailure(err)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.hold != queueRunning {
		return "", telegramFailure("busy")
	}
	if q.telegram.Status().State != "connected" {
		return "", telegramFailure("login_required")
	}
	if mode == "preview" && q.localPlayback != nil {
		q.localPlayback.cancel()
		q.localPlayback = nil
	}
	// The bundled tdl processes share an exclusive session database.
	// The queue worker starts a Telegram task once that database is free.
	if q.telegramTask != nil {
		q.telegramTask.cancel()
	}
	ctx, cancel := context.WithCancel(q.ctx)
	q.telegramTask = &mediaTask{
		id: id, mode: mode, ctx: ctx, cancel: cancel, run: run,
		status: taskStatus{State: "loading"},
	}
	q.notify()
	return id, nil
}

func (q *queue) mediaTaskStatus(mode, id string) (taskStatus, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	task := q.telegramTask
	if mode == "preview" && q.localPlayback != nil && q.localPlayback.id == id {
		task = q.localPlayback
	}
	if task == nil || task.id != id || task.mode != mode {
		return taskStatus{}, telegramFailure("not_found")
	}
	status := task.status
	status.Waiting = status.State == "loading" && !task.begun && q.activeID != ""
	if time.Since(status.lastPreviewRead) > previewSpeedIdle {
		status.Speed = 0
	}
	return status, nil
}

func (q *queue) cancelTelegramTask(id string, preview bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if preview && q.localPlayback != nil && q.localPlayback.id == id {
		q.localPlayback.cancel()
		q.localPlayback = nil
		return
	}
	task := q.telegramTask
	if task != nil && task.id == id && (task.mode == "preview") == preview {
		task.cancel()
		q.telegramTask = nil
		q.notify()
	}
}

func (q *queue) runMediaTask(task *mediaTask, current **mediaTask) {
	defer task.cancel()
	update := func(change func(*taskStatus)) {
		q.mu.Lock()
		defer q.mu.Unlock()
		if *current == task && task.ctx.Err() == nil {
			change(&task.status)
		}
	}
	result, err := task.run(task.ctx, update)
	if err != nil {
		result = taskStatus{State: "failed", Error: transferFailure(err)}
	} else {
		result.State = "ready"
	}
	update(func(status *taskStatus) { *status = result })
}
