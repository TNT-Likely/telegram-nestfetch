// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
)

func (q *queue) setJobHidden(id string, hidden bool) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.byIDLocked(id)
	if entry == nil {
		return &Failure{Code: "not_found"}
	}
	if entry.State != completed {
		return &Failure{Code: "invalid_state"}
	}
	if entry.Hidden == hidden {
		return nil
	}
	return q.updateLocked(entry, func(job *queueEntry) { job.Hidden = hidden })
}

func (q *queue) removeJob(ctx context.Context, id string, discardStaged bool) error {
	done, err := q.beginRemoval(id, discardStaged)
	if err != nil {
		return err
	}
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return &Failure{Code: "busy"}
		}
	}

	q.mu.Lock()
	defer q.mu.Unlock()
	if entry := q.byIDLocked(id); entry != nil {
		return q.finishRemovalLocked(entry)
	}
	return nil
}

func (q *queue) beginRemoval(id string, discardStaged bool) (<-chan struct{}, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	entry := q.byIDLocked(id)
	if entry == nil {
		return nil, &Failure{Code: "not_found"}
	}
	switch entry.State {
	case saving, completed:
		return nil, &Failure{Code: "invalid_state"}
	case removing:
	default:
		if entry.TransferDone && !discardStaged {
			return nil, &Failure{Code: "discard_staged_required"}
		}
		// Commit the removal intent before stopping the worker, so the download
		// cannot be retried or restarted if the app closes during cleanup.
		if err := q.updateLocked(entry, func(job *queueEntry) {
			job.State, job.Error, job.Speed = removing, nil, 0
		}); err != nil {
			return nil, err
		}
	}
	if q.activeID == id {
		q.cancelJob()
		return q.activeDone, nil
	}
	return nil, nil
}

func (q *queue) finishRemovalLocked(entry *queueEntry) error {
	if err := q.clearJobCache(entry.ID); err != nil {
		entry.Error = localFailure(err)
		return entry.Error
	}
	previous := q.state.Jobs
	index := slices.Index(previous, entry)
	q.state.Jobs = slices.Delete(slices.Clone(previous), index, index+1)
	if err := q.persistLocked(); err != nil {
		q.state.Jobs = previous
		entry.Error = localFailure(err)
		return entry.Error
	}
	return nil
}

func (q *queue) clearJobCache(id string) error {
	root, err := os.OpenRoot(q.dataDir)
	if err != nil {
		return err
	}
	defer root.Close()
	// Removal is confined to this job's assets inside the private app data.
	// The selected download folder and the published file are never opened.
	if err := root.RemoveAll(filepath.Join("staging", id)); err != nil {
		return err
	}
	for _, name := range []string{id + ".txt", id + ".frame.jpg"} {
		if err := root.Remove(filepath.Join("thumbnails", name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (q *queue) recoverRemovals() {
	for _, entry := range slices.Clone(q.state.Jobs) {
		if entry.State == removing {
			_ = q.finishRemovalLocked(entry)
		}
	}
}
