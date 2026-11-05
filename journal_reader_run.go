package casbinbunadapter

import (
	"context"
	"fmt"
	"time"
)

const journalSyncRetryInterval = time.Second

// Run maintains retention and enables notifications or polling according to its options.
// Strict checks do not require Run and continue working after it stops.
func (reader *JournalReader) Run(ctx context.Context) error {
	if !reader.runMu.TryLock() {
		return fmt.Errorf("journal reader is already running")
	}
	defer reader.runMu.Unlock()
	if reader.options.Listen {
		err := reader.checkJournalNotifications(ctx)
		if err != nil {
			reader.setListenStatus(false, err)
			return err
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	wake := make(chan struct{}, 1)
	if reader.options.Listen {
		done := make(chan struct{})
		go func() {
			defer close(done)
			reader.listenJournal(ctx, wake)
		}()
		defer func() { cancel(); <-done }()
	}
	var nextPoll, nextCleanup, nextSync time.Time
	pending := false
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := time.Now()
		if reader.options.PollInterval > 0 && !now.Before(nextPoll) {
			pending = true
			nextPoll = now.Add(reader.options.PollInterval)
		}
		if pending && !now.Before(nextSync) {
			err := reader.Sync(ctx)
			status := reader.Status()
			if err != nil {
				nextSync = time.Now().Add(journalSyncRetryInterval)
			} else {
				pending = status.Revision < status.Head
				nextSync = time.Time{}
			}
		}
		if ctx.Err() == nil && !time.Now().Before(nextCleanup) {
			cleanupCtx, cleanupCancel := context.WithTimeout(ctx, reader.options.Timeout)
			removed, err := reader.adapter.PruneJournal(cleanupCtx, reader.schema)
			cleanupCancel()
			reader.viewMu.Lock()
			reader.status.CleanupError = err
			reader.viewMu.Unlock()
			nextCleanup = time.Now().Add(time.Minute)
			if err == nil && removed == journalCleanupBatchLimit {
				nextCleanup = time.Now()
			}
		}
		delay := time.Until(nextCleanup)
		if reader.options.PollInterval > 0 {
			delay = min(delay, time.Until(nextPoll))
		}
		if pending {
			delay = min(delay, time.Until(nextSync))
		}
		timer := time.NewTimer(max(delay, 0))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-wake:
			timer.Stop()
			pending = true
		case <-timer.C:
		}
	}
}

func wakeJournalReader(wake chan<- struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}
