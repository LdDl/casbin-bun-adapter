package casbinbunadapter

import (
	"context"
	"fmt"
	"time"
)

// Enforce checks against policies at least as recent as the journal position read for this call.
// The adapter must connect to the primary PostgreSQL database.
func (reader *JournalReader) Enforce(values ...interface{}) (bool, error) {
	return reader.EnforceContext(context.Background(), values...)
}

// EnforceContext synchronizes to a fixed journal position before evaluating the request.
// Context cancellation or a synchronization error returns false and an error.
func (reader *JournalReader) EnforceContext(ctx context.Context, values ...interface{}) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, reader.options.Timeout)
	defer cancel()
	err := ctx.Err()
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrJournalNotReady, err)
	}
	target, err := reader.readHead(ctx)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrJournalNotReady, err)
	}
	err = reader.synchronizeTo(ctx, target)
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrJournalNotReady, err)
	}
	reader.viewMu.RLock()
	ready := reader.hasRevision(target)
	view := reader.view
	if ready {
		view.users.Add(1)
	}
	reader.viewMu.RUnlock()
	if ready {
		defer view.users.Add(-1)
	}
	err = ctx.Err()
	if err != nil {
		return false, fmt.Errorf("%w: %w", ErrJournalNotReady, err)
	}
	if !ready {
		return false, fmt.Errorf("%w: authorization view changed during synchronization", ErrJournalNotReady)
	}
	allowed, err := view.enforcer.Enforce(values...)
	contextErr := ctx.Err()
	if contextErr != nil {
		return false, fmt.Errorf("%w: %w", ErrJournalNotReady, contextErr)
	}
	if err != nil {
		return false, err
	}
	return allowed, nil
}

// hasRevision requires at least a read lock on viewMu.
func (reader *JournalReader) hasRevision(target journalHead) bool {
	return reader.view != nil && reader.status.Error == nil &&
		reader.identity == target.Identity && reader.status.Revision >= target.Revision
}

func (reader *JournalReader) synchronizeTo(ctx context.Context, target journalHead) error {
	reader.viewMu.RLock()
	ready := reader.hasRevision(target)
	reader.viewMu.RUnlock()
	if ready {
		return nil
	}
	err := reader.acquireSync(ctx)
	if err != nil {
		return err
	}
	defer reader.releaseSync()
	for {
		err = ctx.Err()
		if err != nil {
			return err
		}
		reader.viewMu.RLock()
		ready = reader.hasRevision(target)
		reader.viewMu.RUnlock()
		if ready {
			return nil
		}
		err = reader.synchronize(ctx, &target)
		if err != nil {
			return err
		}
	}
}

// EnforceCached checks only the local view and may miss recently committed changes.
// Synchronization errors, known unfinished catch-up and MaxStaleness block the check.
func (reader *JournalReader) EnforceCached(values ...interface{}) (bool, error) {
	reader.viewMu.RLock()
	var err error
	switch {
	case reader.status.Error != nil:
		err = fmt.Errorf("%w: %w", ErrJournalNotReady, reader.status.Error)
	case reader.status.Revision < reader.status.Head:
		err = fmt.Errorf("%w: catching up with committed policies", ErrJournalNotReady)
	case reader.view == nil || reader.identity == 0 || time.Since(reader.status.LastSync) > reader.options.MaxStaleness:
		err = fmt.Errorf("%w: policies exceeded maximum age", ErrJournalNotReady)
	}
	view := reader.view
	if err == nil {
		view.users.Add(1)
	}
	reader.viewMu.RUnlock()
	if err != nil {
		return false, err
	}
	defer view.users.Add(-1)
	return view.enforcer.Enforce(values...)
}
