// SPDX-License-Identifier: MIT
package main

type queueHold uint8

const (
	queueRunning queueHold = iota
	holdForProxy
	holdForUpdate
)

func (q *queue) engineBusyLocked() bool {
	if q.hold != queueRunning || q.activeID != "" || q.accountBusy ||
		(q.telegramTask != nil && q.telegramTask.ctx.Err() == nil) ||
		(q.localPlayback != nil && q.localPlayback.ctx.Err() == nil) {
		return true
	}
	switch q.telegram.Status().State {
	case "starting", "qr", "password":
		return true
	default:
		return false
	}
}

func (q *queue) prepareUpdate() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.engineBusyLocked() {
		return telegramFailure("update_busy")
	}
	q.hold = holdForUpdate
	return nil
}

func (q *queue) finishUpdate() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.hold == holdForUpdate {
		q.hold = queueRunning
		q.notify()
	}
}
