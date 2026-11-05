package casbinbunadapter

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

// ErrJournalNotReady indicates that authorization data cannot currently be trusted.
var ErrJournalNotReady = errors.New("policy journal is not synchronized")

// JournalReaderOptions controls synchronization and explicitly cached checks.
type JournalReaderOptions struct {
	// PollInterval enables background synchronization when positive. Zero disables polling.
	PollInterval time.Duration
	// Timeout bounds database synchronization, including waiting for another updater.
	Timeout time.Duration
	// MaxStaleness limits the age of policies used by EnforceCached.
	MaxStaleness time.Duration
	// BatchLimit limits the number of complete transactions read in one synchronization step.
	BatchLimit int
	// Configure runs on each new enforcer before its role links are built.
	Configure func(*casbin.Enforcer) error
}

// JournalStatus describes the last synchronization attempt.
type JournalStatus struct {
	Revision     int64
	Head         int64
	LastSync     time.Time
	Error        error
	CleanupError error
}

// JournalReader owns a read-only authorization view; policies are changed in PostgreSQL.
type JournalReader struct {
	adapter  *BunAdapter
	schema   string
	options  JournalReaderOptions
	template model.Model
	model    model.Model
	identity int64
	syncGate chan struct{}
	runMu    sync.Mutex
	viewMu   sync.RWMutex
	view     *journalView
	spare    *journalView
	status   JournalStatus
}

// NewJournalReader loads policies and the journal position from one database snapshot.
func (adapter *BunAdapter) NewJournalReader(ctx context.Context, schemaName string, m model.Model, options JournalReaderOptions) (*JournalReader, error) {
	err := validateJournalIdentifier(schemaName)
	if err != nil {
		return nil, err
	}
	if len(m["r"]) == 0 || len(m["p"]) == 0 || len(m["e"]) == 0 || len(m["m"]) == 0 {
		return nil, fmt.Errorf("journal reader requires a complete Casbin model")
	}
	if options.Timeout == 0 {
		options.Timeout = 5 * time.Second
	}
	if options.MaxStaleness == 0 {
		options.MaxStaleness = 30 * time.Second
	}
	if options.BatchLimit == 0 {
		options.BatchLimit = 100
	}
	if options.PollInterval < 0 || options.Timeout <= 0 || options.MaxStaleness <= 0 || options.BatchLimit < 1 {
		return nil, fmt.Errorf("invalid journal reader options")
	}
	template := copyJournalModel(m)
	template.ClearPolicy()
	reader := &JournalReader{
		adapter:  adapter,
		schema:   schemaName,
		options:  options,
		template: template,
		syncGate: make(chan struct{}, 1),
	}
	err = reader.Sync(ctx)
	if err != nil {
		return nil, err
	}
	return reader, nil
}

// Sync reads a bounded group of complete transactions and publishes their result atomically.
// A failed attempt leaves the cursor unchanged and suspends authorization until recovery.
func (reader *JournalReader) Sync(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, reader.options.Timeout)
	defer cancel()
	err := reader.acquireSync(ctx)
	if err != nil {
		return err
	}
	defer reader.releaseSync()
	return reader.synchronize(ctx, nil)
}

func (reader *JournalReader) acquireSync(ctx context.Context) error {
	select {
	case reader.syncGate <- struct{}{}:
		err := ctx.Err()
		if err != nil {
			reader.releaseSync()
		}
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (reader *JournalReader) releaseSync() {
	<-reader.syncGate
}

// synchronize requires exclusive ownership of syncGate.
func (reader *JournalReader) synchronize(ctx context.Context, target *journalHead) error {
	started := time.Now()
	state := reader.Status()
	update, err := reader.read(ctx, state.Revision, target)
	if err != nil {
		return reader.recordError(err)
	}
	var publication *journalPublication
	if update.Model != nil || len(update.Changes) != 0 {
		publication, err = reader.preparePublication(ctx, update)
		if err != nil {
			return reader.recordError(err)
		}
	}
	err = ctx.Err()
	if err != nil {
		return reader.recordError(err)
	}
	if publication != nil {
		// Validation and private enforcer updates precede changes to the canonical model.
		err = applyJournalModelMutations(publication.Model, publication.Mutations)
		if err != nil {
			reader.viewMu.Lock()
			reader.identity = 0
			reader.viewMu.Unlock()
			return reader.recordError(err)
		}
	}
	reader.viewMu.Lock()
	if publication != nil {
		reader.view = publication.View
		reader.spare = publication.Spare
		if reader.spare != nil {
			reader.spare.replay = publication.Replay
		}
		reader.model = publication.Model
		reader.identity = update.Identity
		reader.status.Revision = update.Revision
	}
	reader.status.Error = nil
	reader.status.Head = update.Head
	// A partial catch-up must not extend the lifetime of stale permissions.
	if update.Revision == update.Head {
		reader.status.LastSync = started
	}
	reader.viewMu.Unlock()
	return nil
}

// Reload replaces local policies and the cursor with a fresh, consistent database snapshot.
func (reader *JournalReader) Reload(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, reader.options.Timeout)
	defer cancel()
	err := reader.acquireSync(ctx)
	if err != nil {
		return err
	}
	defer reader.releaseSync()
	// Reload the policy table even if a retained journal event is invalid.
	reader.viewMu.Lock()
	reader.identity = 0
	reader.viewMu.Unlock()
	return reader.synchronize(ctx, nil)
}

// Run maintains journal retention and optionally polls when PollInterval is positive.
// Strict checks do not require Run and continue working after it stops.
func (reader *JournalReader) Run(ctx context.Context) error {
	if !reader.runMu.TryLock() {
		return fmt.Errorf("journal reader is already running")
	}
	defer reader.runMu.Unlock()
	interval := reader.options.PollInterval
	if interval == 0 || interval > time.Minute {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	var cleanedAt time.Time
	var polledAt time.Time
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if reader.options.PollInterval > 0 && time.Since(polledAt) >= reader.options.PollInterval {
			_ = reader.Sync(ctx)
			status := reader.Status()
			if status.Error != nil || status.Revision >= status.Head {
				polledAt = time.Now()
			}
		}
		if ctx.Err() == nil && time.Since(cleanedAt) >= time.Minute {
			cleanupCtx, cancel := context.WithTimeout(ctx, reader.options.Timeout)
			removed, cleanupErr := reader.adapter.PruneJournal(cleanupCtx, reader.schema)
			cancel()
			reader.viewMu.Lock()
			reader.status.CleanupError = cleanupErr
			reader.viewMu.Unlock()
			if cleanupErr != nil || removed < journalCleanupBatchLimit {
				cleanedAt = time.Now()
			}
		}
		status := reader.Status()
		if reader.options.PollInterval > 0 && ctx.Err() == nil && status.Error == nil && status.Revision < status.Head {
			polledAt = time.Time{}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Status returns the current cursor, synchronization time and errors.
func (reader *JournalReader) Status() JournalStatus {
	reader.viewMu.RLock()
	defer reader.viewMu.RUnlock()
	return reader.status
}

func (reader *JournalReader) recordError(err error) error {
	reader.viewMu.Lock()
	reader.status.Error = err
	reader.viewMu.Unlock()
	return err
}
