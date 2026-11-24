// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	queued      = "queued"
	resolving   = "resolving"
	downloading = "downloading"
	paused      = "paused"
	saving      = "saving"
	waitingSave = "waiting_save"
	completed   = "completed"
	failed      = "failed"
	removing    = "removing"
)

type Job struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	Title        string    `json:"title"`
	Source       string    `json:"source"`
	Name         string    `json:"name"`
	Kind         MediaKind `json:"kind,omitempty"`
	Size         int64     `json:"size"`
	Duration     float64   `json:"duration"`
	Downloaded   int64     `json:"downloaded"`
	Speed        int64     `json:"speed"`
	State        string    `json:"state"`
	Folder       string    `json:"folder"`
	OutputPath   string    `json:"outputPath"`
	Error        *Failure  `json:"error,omitempty"`
	HasThumbnail bool      `json:"hasThumbnail"`
	Hidden       bool      `json:"hidden,omitempty"`
	TransferDone bool      `json:"transferDone,omitempty"`
}

type queueEntry struct {
	Job
	TopicID       int         `json:"topicId,omitempty"`
	DocumentID    int64       `json:"documentId,omitempty"`
	HideThumbnail bool        `json:"hideThumbnail,omitempty"`
	Save          *saveIntent `json:"save,omitempty"`
}

type queueState struct {
	Version int           `json:"version"`
	Folder  string        `json:"folder"`
	Jobs    []*queueEntry `json:"jobs"`
}

type queueSnapshot struct {
	Account  Account  `json:"account"`
	Settings settings `json:"settings"`
	Jobs     []Job    `json:"jobs"`
}

type settings struct {
	Folder string              `json:"folder"`
	Proxy  telegramProxyStatus `json:"proxy"`
}

type queue struct {
	mu            sync.Mutex
	state         queueState
	statePath     string
	dataDir       string
	frameHelper   string
	telegram      Telegram
	proxyAddress  *url.URL
	hold          queueHold
	activeID      string
	cancelJob     context.CancelFunc
	activeDone    chan struct{}
	lastBytesAt   time.Time
	accountBusy   bool
	telegramTask  *mediaTask
	localPlayback *mediaTask

	ctx     context.Context
	cancel  context.CancelFunc
	wake    chan struct{}
	workers sync.WaitGroup
}

func openQueue(ctx context.Context, dataDir, downloadDir, frameHelper string, telegram Telegram) (*queue, error) {
	for _, path := range []string{dataDir, downloadDir} {
		if !filepath.IsAbs(path) {
			return nil, errors.New("queue directories must use absolute paths")
		}
	}
	if frameHelper != "" && !filepath.IsAbs(frameHelper) {
		return nil, errors.New("frame helper must use an absolute path")
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "staging"), 0700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dataDir, "thumbnails"), 0700); err != nil {
		return nil, err
	}
	proxyAddress, err := readTelegramProxy(dataDir)
	if err != nil {
		return nil, err
	}
	worker, cancel := context.WithCancel(ctx)
	q := &queue{
		state:        queueState{Version: 1, Folder: filepath.Clean(downloadDir), Jobs: make([]*queueEntry, 0)},
		statePath:    filepath.Join(dataDir, "queue.json"),
		dataDir:      dataDir,
		frameHelper:  frameHelper,
		telegram:     telegram,
		proxyAddress: proxyAddress,
		ctx:          worker,
		cancel:       cancel,
		wake:         make(chan struct{}, 1),
	}
	if err := q.load(); err != nil {
		cancel()
		return nil, err
	}
	q.recoverRemovals()
	q.startThumbnailRecovery()
	q.workers.Go(q.run)
	return q, nil
}

func (q *queue) load() error {
	file, err := os.Open(q.statePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()

	var existing queueState
	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&existing); err != nil {
		return fmt.Errorf("read queue state: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after queue state")
	}
	if existing.Version != 1 || !filepath.IsAbs(existing.Folder) {
		return errors.New("unsupported or invalid queue state")
	}

	seen := make(map[string]struct{}, len(existing.Jobs))
	for _, entry := range existing.Jobs {
		if entry == nil || !validID(entry.ID) || !filepath.IsAbs(entry.Folder) {
			return errors.New("invalid persisted download")
		}
		if _, duplicate := seen[entry.ID]; duplicate {
			return errors.New("duplicate persisted download ID")
		}
		seen[entry.ID] = struct{}{}
		if _, err := parseMessageLink(entry.URL); err != nil {
			return errors.New("invalid persisted Telegram link")
		}
		if entry.Save != nil && !entry.Save.valid() {
			return errors.New("invalid persisted save destination")
		}
		if entry.DocumentID != 0 && !validOutputName(entry.Name) {
			return errors.New("invalid persisted video filename")
		}
		if entry.OutputPath != "" &&
			(!validOutputName(filepath.Base(entry.OutputPath)) ||
				entry.OutputPath != filepath.Join(entry.Folder, filepath.Base(entry.OutputPath))) {
			return errors.New("invalid persisted output path")
		}
		entry.Speed = 0
		if entry.Kind == "" && entry.Size > 0 {
			// Earlier queue records stored videos only.
			entry.Kind = MediaVideo
		}
		if entry.HideThumbnail || (entry.HasThumbnail && !fileExists(q.thumbnailPath(entry.ID))) {
			entry.HasThumbnail = false
		}
		// A forced app exit can leave a tdl work directory beside the part.
		// Only the canonical part can be published or reused by the queue.
		_ = discardTDLTransferDirs(filepath.Dir(q.partPath(entry.ID)))
		switch entry.State {
		case resolving, downloading, saving:
			entry.State = queued
		case queued, paused, waitingSave, completed, failed, removing:
		default:
			return errors.New("invalid persisted download state")
		}

		if entry.State != completed {
			entry.Downloaded = partSize(q.partPath(entry.ID))
			continue
		}
		// A completed output was committed before this part became redundant.
		if entry.OutputPath != "" && partSize(q.partPath(entry.ID)) != 0 {
			q.cleanPartIfOutputExists(entry)
		}
	}
	q.state = existing
	return nil
}

func (q *queue) close() {
	q.mu.Lock()
	q.cancel()
	q.mu.Unlock()
	q.workers.Wait()
}

func (q *queue) notify() {
	select {
	case q.wake <- struct{}{}:
	default:
	}
}

func (q *queue) snapshot() queueSnapshot {
	account := q.telegram.Status()
	q.mu.Lock()
	defer q.mu.Unlock()
	jobs := make([]Job, 0, len(q.state.Jobs))
	for i := len(q.state.Jobs) - 1; i >= 0; i-- {
		job := q.state.Jobs[i].Job
		if job.Speed != 0 && time.Since(q.lastBytesAt) > 2*time.Second {
			job.Speed = 0
		}
		jobs = append(jobs, job)
	}
	return queueSnapshot{
		Account:  account,
		Settings: settings{Folder: q.state.Folder, Proxy: publicTelegramProxy(q.proxyAddress)},
		Jobs:     jobs,
	}
}

func (q *queue) persistLocked() error {
	return writePrivateFile(q.statePath, func(writer io.Writer) error {
		return json.NewEncoder(writer).Encode(&q.state)
	})
}

func writePrivateFile(path string, write func(io.Writer) error) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".nestfetch-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err = write(file); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	syncDirectory(directory)
	return nil
}

func syncDirectory(path string) {
	directory, err := os.Open(path)
	if err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
}

func randomID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func localFailure(err error) *Failure {
	return &Failure{Code: "local_storage", Message: err.Error()}
}

func transferFailure(err error) *Failure {
	var known *Failure
	if errors.As(err, &known) {
		return known
	}
	return &Failure{Code: "telegram_error"}
}

func (q *queue) byIDLocked(id string) *queueEntry {
	for _, job := range q.state.Jobs {
		if job.ID == id {
			return job
		}
	}
	return nil
}

// updateLocked restores the previous in-memory state if it wasn't committed.
func (q *queue) updateLocked(entry *queueEntry, update func(*queueEntry)) error {
	before := *entry
	update(entry)
	if err := q.persistLocked(); err != nil {
		*entry = before
		return localFailure(err)
	}
	return nil
}

func (q *queue) setFolder(path string) error {
	folder, err := checkFolder(path)
	if err != nil {
		return err
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	previous := q.state.Folder
	q.state.Folder = folder
	if err := q.persistLocked(); err != nil {
		q.state.Folder = previous
		return localFailure(err)
	}
	return nil
}

func (q *queue) accountAction(action func() error) error {
	q.mu.Lock()
	if q.activeID != "" || q.accountBusy || q.hold != queueRunning {
		q.mu.Unlock()
		return &Failure{Code: "busy"}
	}
	q.accountBusy = true
	q.mu.Unlock()

	err := action()

	q.mu.Lock()
	q.accountBusy = false
	q.mu.Unlock()
	q.notify()
	return err
}

func (q *queue) enqueue(rawURL string) (string, error) {
	link, err := parseMessageLink(rawURL)
	if err != nil {
		return "", err
	}
	added, _, err := q.enqueueLinks([]MessageLink{link})
	if err != nil {
		return "", err
	}
	if len(added) == 0 {
		return "", &Failure{Code: "duplicate"}
	}
	return added[0], nil
}

const maxDownloadBatch = 60

type batchResult struct {
	Added      int `json:"added"`
	Duplicates int `json:"duplicates"`
}

func (q *queue) enqueueBatch(urls []string) (batchResult, error) {
	if len(urls) == 0 || len(urls) > maxDownloadBatch {
		return batchResult{}, &Failure{Code: "batch_limit"}
	}
	links := make([]MessageLink, 0, len(urls))
	for _, raw := range urls {
		link, err := parseMessageLink(raw)
		if err != nil {
			return batchResult{}, err
		}
		links = append(links, link)
	}
	added, duplicates, err := q.enqueueLinks(links)
	return batchResult{Added: len(added), Duplicates: duplicates}, err
}

func (q *queue) enqueueLinks(links []MessageLink) ([]string, int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	seen := make(map[string]bool, len(links))
	for _, link := range links {
		seen[link.URL] = false
	}
	for _, job := range q.state.Jobs {
		if _, requested := seen[job.URL]; !requested {
			continue
		}
		if job.State != completed || (job.Folder == q.state.Folder && fileExists(job.OutputPath)) {
			seen[job.URL] = true
		}
	}

	jobs := make([]*queueEntry, 0, len(links))
	ids := make([]string, 0, len(links))
	for _, link := range links {
		if seen[link.URL] {
			continue
		}
		id, err := randomID()
		if err != nil {
			return nil, 0, localFailure(err)
		}
		seen[link.URL] = true
		ids = append(ids, id)
		jobs = append(jobs, &queueEntry{
			Job: Job{ID: id, URL: link.URL, State: queued, Folder: q.state.Folder}, TopicID: link.TopicID,
		})
	}
	duplicates := len(links) - len(jobs)
	if len(jobs) == 0 {
		return ids, duplicates, nil
	}
	previous := q.state.Jobs
	q.state.Jobs = append(previous, jobs...)
	if err := q.persistLocked(); err != nil {
		q.state.Jobs = previous
		return nil, 0, localFailure(err)
	}
	q.notify()
	return ids, duplicates, nil
}

func (q *queue) pause(id string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.byIDLocked(id)
	if entry == nil {
		return &Failure{Code: "not_found"}
	}
	switch entry.State {
	case paused:
		return nil
	case queued, resolving, downloading:
	default:
		return &Failure{Code: "invalid_state"}
	}
	if err := q.updateLocked(entry, func(entry *queueEntry) {
		entry.State, entry.Error, entry.Speed = paused, nil, 0
	}); err != nil {
		return err
	}
	if q.activeID == id {
		q.cancelJob()
	}
	return nil
}

func (q *queue) resume(id string) error {
	return q.requeue(id, "", paused)
}

func (q *queue) retry(id string) error {
	return q.requeue(id, "", waitingSave, failed)
}

func (q *queue) saveElsewhere(id, path string) error {
	folder, err := checkFolder(path)
	if err != nil {
		return err
	}
	return q.requeue(id, folder, waitingSave)
}

func (q *queue) requeue(id, folder string, allowed ...string) error {
	q.mu.Lock()
	entry := q.byIDLocked(id)
	if entry == nil {
		q.mu.Unlock()
		return &Failure{Code: "not_found"}
	}
	currentlyAllowed := false
	for _, state := range allowed {
		currentlyAllowed = currentlyAllowed || state == entry.State
	}
	if !currentlyAllowed {
		q.mu.Unlock()
		return &Failure{Code: "invalid_state"}
	}
	var obsolete *saveIntent
	oldFolder := entry.Folder
	err := q.updateLocked(entry, func(entry *queueEntry) {
		entry.State, entry.Error = queued, nil
		if folder != "" && entry.Folder != folder {
			obsolete = entry.Save
			entry.Folder, entry.Save = folder, nil
		}
	})
	q.mu.Unlock()
	if err != nil {
		return err
	}
	if obsolete != nil && obsolete.Temporary != "" {
		removeTemporary(oldFolder, obsolete.Temporary)
	}
	q.notify()
	return nil
}

func (q *queue) revealPath(id string) (string, error) {
	q.mu.Lock()
	entry := q.byIDLocked(id)
	if entry == nil {
		q.mu.Unlock()
		return "", &Failure{Code: "not_found"}
	}
	state, path, folder := entry.State, entry.OutputPath, entry.Folder
	q.mu.Unlock()
	if state != completed || path == "" {
		return "", &Failure{Code: "invalid_state"}
	}
	if _, err := checkFolder(folder); err != nil {
		return "", err
	}
	if !fileExists(path) {
		return "", &Failure{Code: "file_missing"}
	}
	return path, nil
}

func (q *queue) partPath(id string) string {
	return filepath.Join(q.dataDir, "staging", id, "download.part")
}

func (q *queue) cleanPartIfOutputExists(entry *queueEntry) {
	if _, err := checkFolder(entry.Folder); err != nil {
		return
	}
	stat, err := os.Lstat(entry.OutputPath)
	if err == nil && stat.Mode().IsRegular() && stat.Size() == entry.Size {
		// Only remove the staged data we own. The mounted folder can disappear
		// after this check, but the file has already been committed there.
		_ = os.Remove(q.partPath(entry.ID))
		_ = os.Remove(filepath.Dir(q.partPath(entry.ID)))
	}
}

func partSize(path string) int64 {
	stat, err := os.Lstat(path)
	if err != nil || !stat.Mode().IsRegular() {
		return 0
	}
	return stat.Size()
}

func fileExists(path string) bool {
	stat, err := os.Lstat(path)
	return err == nil && stat.Mode().IsRegular()
}

func (q *queue) run() {
	ticker := time.NewTicker(400 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-q.ctx.Done():
			return
		case <-q.wake:
		case <-ticker.C:
		}
		for q.ctx.Err() == nil {
			if !q.workNext() {
				break
			}
		}
	}
}

func (q *queue) workNext() bool {
	q.mu.Lock()
	if q.accountBusy || q.hold != queueRunning {
		q.mu.Unlock()
		return false
	}
	if task := q.telegramTask; task != nil && !task.begun {
		task.begun = true
		q.accountBusy = true
		q.mu.Unlock()
		q.runMediaTask(task, &q.telegramTask)
		q.mu.Lock()
		q.accountBusy = false
		q.mu.Unlock()
		return true
	}
	connected := q.telegram.Status().State == "connected"
	var chosen *queueEntry
	for _, entry := range q.state.Jobs {
		if entry.State == queued && (connected || entry.TransferDone) {
			chosen = entry
			break
		}
	}
	if chosen == nil {
		q.mu.Unlock()
		return false
	}

	state := resolving
	if chosen.TransferDone {
		state = saving
	}
	if err := q.updateLocked(chosen, func(entry *queueEntry) {
		entry.State, entry.Error = state, nil
	}); err != nil {
		chosen.State, chosen.Error = failed, transferFailure(err)
		q.mu.Unlock()
		return true
	}
	ctx, cancel := context.WithCancel(q.ctx)
	done := make(chan struct{})
	q.activeID, q.cancelJob, q.activeDone = chosen.ID, cancel, done
	job := *chosen
	q.mu.Unlock()

	q.process(ctx, job)
	cancel()
	q.mu.Lock()
	q.activeID, q.cancelJob, q.activeDone = "", nil, nil
	close(done)
	q.mu.Unlock()
	return true
}

func (q *queue) process(ctx context.Context, entry queueEntry) {
	if !entry.TransferDone {
		var ok bool
		entry, ok = q.fetch(ctx, entry)
		if !ok {
			return
		}
	}
	if entry.localThumbnailAllowed() && q.frameHelper != "" {
		if q.extractThumbnail(ctx, entry) &&
			!q.jobUpdate(ctx, entry.ID, func(current *queueEntry) { current.HasThumbnail = true }) {
			return
		}
	}
	if ctx.Err() != nil {
		return
	}
	if err := q.publish(ctx, entry); err != nil {
		if transferFailure(err).Code == "download_incomplete" {
			q.jobUpdate(ctx, entry.ID, func(job *queueEntry) {
				job.TransferDone, job.State, job.Error = false, failed, transferFailure(err)
				job.Downloaded = partSize(q.partPath(job.ID))
			})
			return
		}
		q.setWorkerError(ctx, entry.ID, waitingSave, err)
	}
}

func (q *queue) fetch(ctx context.Context, entry queueEntry) (queueEntry, bool) {
	link, err := parseMessageLink(entry.URL)
	if err != nil {
		q.setWorkerError(ctx, entry.ID, failed, err)
		return entry, false
	}
	if entry.TopicID != 0 {
		link.TopicID = entry.TopicID
	}

	inspectCtx, cancel := context.WithTimeout(ctx, 50*time.Second)
	media, err := q.telegram.Inspect(inspectCtx, link)
	cancel()
	if err != nil {
		q.setWorkerError(ctx, entry.ID, failed, err)
		return entry, false
	}
	if media.ID == 0 || media.Bytes <= 0 {
		q.setWorkerError(ctx, entry.ID, failed, &Failure{Code: "no_media"})
		return entry, false
	}
	if math.IsNaN(media.Duration) || math.IsInf(media.Duration, 0) || media.Duration < 0 {
		media.Duration = 0
	}
	if ctx.Err() != nil {
		return entry, false
	}

	part := q.partPath(entry.ID)
	if err := os.MkdirAll(filepath.Dir(part), 0700); err != nil {
		q.setWorkerError(ctx, entry.ID, failed, localFailure(err))
		return entry, false
	}
	// A different Telegram document can never reuse the prefix of an older one.
	currentSize := partSize(part)
	replaced := entry.DocumentID != media.ID || entry.Size != media.Bytes || entry.Kind != media.Kind
	if replaced || currentSize > media.Bytes {
		if err := os.Remove(part); err != nil && !errors.Is(err, os.ErrNotExist) {
			q.setWorkerError(ctx, entry.ID, failed, localFailure(err))
			return entry, false
		}
		currentSize = 0
	}
	hasThumbnail := false
	if !media.HideThumbnail {
		hasThumbnail = (entry.HasThumbnail && !replaced) ||
			q.saveThumbnail(entry.ID, media.Thumbnail)
	} else {
		_ = os.Remove(q.thumbnailPath(entry.ID))
	}

	if !q.jobUpdate(ctx, entry.ID, func(job *queueEntry) {
		job.Name, job.Size, job.Title = filename(media.Name), media.Bytes, strings.TrimSpace(media.Title)
		job.Kind = media.Kind
		job.Source, job.Duration, job.DocumentID = media.Source, media.Duration, media.ID
		job.HasThumbnail, job.Downloaded, job.State = hasThumbnail, currentSize, downloading
		job.HideThumbnail = media.HideThumbnail
		if replaced {
			job.Save = nil
		}
	}) {
		return entry, false
	}
	if replaced && entry.Save != nil {
		removeTemporary(entry.Folder, entry.Save.Temporary)
	}

	lastTime, lastSize := time.Now(), currentSize
	report := func(count int64) {
		q.mu.Lock()
		defer q.mu.Unlock()
		job := q.byIDLocked(entry.ID)
		if job == nil || job.State != downloading || ctx.Err() != nil {
			return
		}
		now := time.Now()
		if count != job.Downloaded {
			q.lastBytesAt = now
		}
		job.Downloaded = max(0, min(count, media.Bytes))
		if elapsed := now.Sub(lastTime).Seconds(); elapsed >= 0.45 {
			job.Speed = max(int64(float64(job.Downloaded-lastSize)/elapsed), 0)
			lastTime, lastSize = now, job.Downloaded
		}
	}
	if err := q.telegram.Download(ctx, link, media, part, report); err != nil {
		q.setWorkerError(ctx, entry.ID, failed, err)
		return entry, false
	}
	if partSize(part) != media.Bytes {
		q.setWorkerError(ctx, entry.ID, failed, &Failure{Code: "download_incomplete"})
		return entry, false
	}

	ok := q.jobUpdate(ctx, entry.ID, func(job *queueEntry) {
		job.Downloaded, job.Speed, job.State, job.TransferDone = media.Bytes, 0, saving, true
		entry = *job
	})
	return entry, ok
}

func (q *queue) jobUpdate(ctx context.Context, id string, update func(*queueEntry)) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.byIDLocked(id)
	if entry == nil || ctx.Err() != nil || (entry.State != resolving && entry.State != downloading && entry.State != saving) {
		return false
	}
	if err := q.updateLocked(entry, update); err != nil {
		entry.State, entry.Error, entry.Speed = failed, transferFailure(err), 0
		return false
	}
	return true
}

func (q *queue) setWorkerError(ctx context.Context, id, state string, err error) {
	// Shutdown leaves an in-progress job resumable. A manual pause already
	// persisted its state before canceling the operation.
	if ctx.Err() != nil {
		return
	}
	failed := transferFailure(err)
	q.jobUpdate(ctx, id, func(entry *queueEntry) {
		entry.State, entry.Error, entry.Speed = state, failed, 0
		entry.Downloaded = partSize(q.partPath(id))
	})
}
