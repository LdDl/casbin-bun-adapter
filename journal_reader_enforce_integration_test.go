package casbinbunadapter

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

type journalDecisionTestResult struct {
	Allowed bool
	Error   error
}

func journalTestEnforceAsync(reader *JournalReader, ctx context.Context) <-chan journalDecisionTestResult {
	done := make(chan journalDecisionTestResult, 1)
	go func() {
		allowed, err := reader.EnforceContext(ctx, "alice", "data", "read")
		done <- journalDecisionTestResult{Allowed: allowed, Error: err}
	}()
	return done
}

func requireJournalStrictDecision(t *testing.T, reader *JournalReader, expected bool) {
	t.Helper()
	allowed, err := reader.Enforce("alice", "data", "read")
	require.NoError(t, err)
	require.Equal(t, expected, allowed)
}

func TestPostgresJournalEnforceSeesCommittedChangesWithoutWorker(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES
		('p','readers','data','read','allow'), ('g','alice','readers',NULL,NULL)`, bun.Name(adapter.matcher.SchemaName))
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	requireJournalStrictDecision(t, reader, true)
	storageTestExec(t, adapter, `DELETE FROM ?.policies WHERE ptype='g'`, bun.Name(adapter.matcher.SchemaName))
	requireJournalCachedDecision(t, reader, true)
	requireJournalStrictDecision(t, reader, false)
	err := adapter.AddPolicy("g", "g", []string{"alice", "readers"})
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, true)
	storageTestExec(t, adapter, `UPDATE ?.policies SET v3='deny' WHERE ptype='p'`, bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, false)
}

func TestPostgresJournalEnforceCatchesUpBeforeAllowing(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{BatchLimit: 1})
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	err = adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "deny"})
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, false)
	require.Equal(t, int64(2), reader.Status().Revision)
}

func TestPostgresJournalEnforceKeepsCapturedTarget(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{BatchLimit: 2})
	err := adapter.AddPolicy("g", "g", []string{"alice", "readers"})
	require.NoError(t, err)
	err = adapter.AddPolicy("g", "g2", []string{"bob", "readers"})
	require.NoError(t, err)
	err = adapter.AddPolicy("p", "p", []string{"readers", "data", "read", "allow"})
	require.NoError(t, err)
	hook := &journalSnapshotHook{started: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(hook.resume) })
	t.Cleanup(resume)
	adapter.AddQueryHook(hook)
	done := journalTestEnforceAsync(reader, context.Background())
	select {
	case <-hook.started:
	case <-time.After(3 * time.Second):
		t.Fatal("head query did not start")
	}
	err = adapter.AddPolicy("p", "p", []string{"readers", "data", "read", "deny"})
	require.NoError(t, err)
	resume()
	result := <-done
	require.NoError(t, result.Error)
	require.True(t, result.Allowed)
	require.Equal(t, int64(3), reader.Status().Revision)
	require.Equal(t, int64(4), reader.Status().Head)
	allowed, err := reader.EnforceCached("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
	requireJournalStrictDecision(t, reader, false)
}

func TestPostgresJournalEnforceDoesNotShareAnOlderHead(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	hook := &journalSnapshotHook{started: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(hook.resume) })
	t.Cleanup(resume)
	adapter.AddQueryHook(hook)
	first := journalTestEnforceAsync(reader, context.Background())
	select {
	case <-hook.started:
	case <-time.After(3 * time.Second):
		t.Fatal("head query did not start")
	}
	err = adapter.RemovePolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, false)
	resume()
	result := <-first
	require.NoError(t, result.Error)
	require.False(t, result.Allowed)
}

func TestPostgresJournalEnforceDeadlineWhileUpdaterIsBlocked(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	building := make(chan struct{}, 1)
	resumeBuild := make(chan struct{})
	resume := sync.OnceFunc(func() { close(resumeBuild) })
	t.Cleanup(resume)
	var block atomic.Bool
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{
		Timeout: 100 * time.Millisecond,
		Configure: func(enforcer *casbin.Enforcer) error {
			if block.Load() {
				building <- struct{}{}
				<-resumeBuild
			}
			return nil
		},
	})
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	block.Store(true)
	updated := make(chan error, 1)
	go func() { updated <- reader.Sync(context.Background()) }()
	select {
	case <-building:
	case <-time.After(3 * time.Second):
		t.Fatal("candidate build did not start")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	checked := journalTestEnforceAsync(reader, ctx)
	select {
	case result := <-checked:
		require.False(t, result.Allowed)
		require.ErrorIs(t, result.Error, context.DeadlineExceeded)
		require.ErrorIs(t, result.Error, ErrJournalNotReady)
	case <-time.After(time.Second):
		t.Fatal("authorization did not respect its deadline while waiting for synchronization")
	}
	err = reader.Reload(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	checked = journalTestEnforceAsync(reader, context.Background())
	select {
	case result := <-checked:
		require.False(t, result.Allowed)
		require.ErrorIs(t, result.Error, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("reader timeout did not bound synchronization wait")
	}
	resume()
	require.ErrorIs(t, <-updated, context.DeadlineExceeded)
	block.Store(false)
	requireJournalStrictDecision(t, reader, true)
}

func TestPostgresJournalEnforceFailsClosedAndRecovers(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	allowed, err := reader.EnforceContext(ctx, "alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, context.Canceled)
	require.NoError(t, reader.Status().Error)
	storageTestExec(t, adapter, "ALTER TABLE ?.state RENAME TO unavailable_state", bun.Name(schema))
	allowed, err = reader.Enforce("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
	storageTestExec(t, adapter, "ALTER TABLE ?.unavailable_state RENAME TO state", bun.Name(schema))
	requireJournalStrictDecision(t, reader, true)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1) VALUES ('p','invalid','arity')`, bun.Name(adapter.matcher.SchemaName))
	allowed, err = reader.Enforce("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
	require.Equal(t, int64(1), reader.Status().Revision)
}

func TestPostgresJournalEnforceDoesNotBlockNewPolicyPublication(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	started := make(chan struct{})
	resumeCheck := make(chan struct{})
	resume := sync.OnceFunc(func() { close(resumeCheck) })
	t.Cleanup(resume)
	var block atomic.Bool
	policyModel, err := model.NewModelFromString(strings.Replace(namedPolicyTestModel, "m = g(", "m = wait() && g(", 1))
	require.NoError(t, err)
	reader, err := adapter.NewJournalReader(context.Background(), schema, policyModel, JournalReaderOptions{
		Configure: func(enforcer *casbin.Enforcer) error {
			enforcer.AddFunction("wait", func(args ...interface{}) (interface{}, error) {
				if block.CompareAndSwap(true, false) {
					close(started)
					<-resumeCheck
				}
				return true, nil
			})
			return nil
		},
	})
	require.NoError(t, err)
	block.Store(true)
	first := journalTestEnforceAsync(reader, context.Background())
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("custom matcher did not start")
	}
	err = adapter.RemovePolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	second := journalTestEnforceAsync(reader, context.Background())
	select {
	case result := <-second:
		require.NoError(t, result.Error)
		require.False(t, result.Allowed)
	case <-time.After(time.Second):
		t.Fatal("an older check blocked publication of the revoked policy")
	}
	resume()
	result := <-first
	require.NoError(t, result.Error)
	require.True(t, result.Allowed)
}

type journalQueryCounterHook struct {
	Queries atomic.Int64
	Heads   atomic.Int64
	Prunes  atomic.Int64
}

func (hook *journalQueryCounterHook) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	hook.Queries.Add(1)
	if strings.Contains(event.Query, "tableoid::bigint AS identity") {
		hook.Heads.Add(1)
	}
	if strings.Contains(event.Query, "prune_changes()") {
		hook.Prunes.Add(1)
	}
	return ctx
}

func (hook *journalQueryCounterHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
}

func TestPostgresJournalEnforceUnchangedStateReadsOnlyHead(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	reader.viewMu.Lock()
	reader.status.LastSync = time.Now().Add(-time.Minute)
	reader.viewMu.Unlock()
	allowed, err := reader.EnforceCached("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
	hook := &journalQueryCounterHook{}
	adapter.AddQueryHook(hook)
	requireJournalStrictDecision(t, reader, true)
	require.Equal(t, int64(1), hook.Queries.Load())
	require.Equal(t, int64(1), hook.Heads.Load())
}

func TestPostgresJournalRunDoesNotPollByDefault(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	require.Zero(t, reader.options.PollInterval)
	hook := &journalQueryCounterHook{}
	adapter.AddQueryHook(hook)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- reader.Run(ctx) }()
	require.Eventually(t, func() bool { return hook.Prunes.Load() > 0 }, time.Second, time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.Zero(t, hook.Heads.Load())
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, true)
}

func TestPostgresJournalEnforceReloadsAfterPruning(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	storageTestExec(t, adapter, "UPDATE ?.batches SET created_at=now()-interval '8 days'", bun.Name(schema))
	removed, err := adapter.PruneJournal(context.Background(), schema)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	requireJournalStrictDecision(t, reader, true)
}
