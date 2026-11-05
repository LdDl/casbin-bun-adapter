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

// JournalReaderOptions controls polling and the maximum age of authorization data.
type JournalReaderOptions struct {
	PollInterval time.Duration
	Timeout      time.Duration
	MaxStaleness time.Duration
	BatchLimit   int
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
	syncMu   sync.Mutex
	runMu    sync.Mutex
	viewMu   sync.RWMutex
	enforcer *casbin.Enforcer
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
	if options.PollInterval == 0 {
		options.PollInterval = time.Second
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
	if options.PollInterval <= 0 || options.Timeout <= 0 || options.MaxStaleness <= options.PollInterval || options.BatchLimit < 1 {
		return nil, fmt.Errorf("invalid journal reader options")
	}
	template := copyJournalModel(m)
	template.ClearPolicy()
	r := &JournalReader{adapter: adapter, schema: schemaName, options: options, template: template}
	err = r.Sync(ctx)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// Sync reads a bounded group of complete transactions and publishes their result atomically.
// A failed attempt leaves the cursor unchanged and suspends authorization until recovery.
func (reader *JournalReader) Sync(ctx context.Context) error {
	reader.syncMu.Lock()
	defer reader.syncMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, reader.options.Timeout)
	defer cancel()
	started := time.Now()
	state := reader.Status()
	update, err := reader.read(ctx, state.Revision)
	if err != nil {
		return reader.recordError(err)
	}
	var candidate *casbin.Enforcer
	if update.Model != nil {
		candidate, err = reader.buildEnforcer(update.Model)
		if err != nil {
			return reader.recordError(err)
		}
	}
	err = ctx.Err()
	if err != nil {
		return reader.recordError(err)
	}
	reader.viewMu.Lock()
	if candidate != nil {
		reader.enforcer = candidate
		reader.model = update.Model
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
	reader.syncMu.Lock()
	// The next Sync starts from the policy table, even if a retained event is invalid.
	reader.identity = 0
	reader.syncMu.Unlock()
	return reader.Sync(ctx)
}

// Run polls until cancellation. Failed attempts remain visible in Status and Enforce.
func (reader *JournalReader) Run(ctx context.Context) error {
	if !reader.runMu.TryLock() {
		return fmt.Errorf("journal reader is already running")
	}
	defer reader.runMu.Unlock()
	ticker := time.NewTicker(reader.options.PollInterval)
	defer ticker.Stop()
	var cleanedAt time.Time
	for {
		_ = reader.Sync(ctx)
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
		if ctx.Err() == nil && status.Error == nil && status.Revision < status.Head {
			continue
		}
		select {
		case <-ctx.Done():
			reader.recordError(ctx.Err())
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Enforce denies authorization with an error while synchronization is unhealthy or stale.
func (reader *JournalReader) Enforce(values ...interface{}) (bool, error) {
	reader.viewMu.RLock()
	defer reader.viewMu.RUnlock()
	if reader.status.Error != nil {
		return false, fmt.Errorf("%w: %w", ErrJournalNotReady, reader.status.Error)
	}
	if reader.status.Revision < reader.status.Head {
		return false, fmt.Errorf("%w: catching up with committed policies", ErrJournalNotReady)
	}
	if reader.enforcer == nil || time.Since(reader.status.LastSync) > reader.options.MaxStaleness {
		return false, fmt.Errorf("%w: policies exceeded maximum age", ErrJournalNotReady)
	}
	return reader.enforcer.Enforce(values...)
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
