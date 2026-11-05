package casbinbunadapter

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

func newJournalTestReader(t *testing.T, adapter *BunAdapter, schema string, options JournalReaderOptions) *JournalReader {
	t.Helper()
	m, err := model.NewModelFromString(namedPolicyTestModel)
	require.NoError(t, err)
	r, err := adapter.NewJournalReader(context.Background(), schema, m, options)
	require.NoError(t, err)
	return r
}

func requireJournalDecision(t *testing.T, r *JournalReader, allowed bool) {
	t.Helper()
	actual, err := r.Enforce("alice", "data", "read")
	require.NoError(t, err)
	require.Equal(t, allowed, actual)
}

func TestPostgresJournalReaderChangesAndRestart(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	err := adapter.AddPolicy("g", "g", []string{"alice", "readers"})
	require.NoError(t, err)
	r := newJournalTestReader(t, adapter, schema, JournalReaderOptions{BatchLimit: 1})
	requireJournalDecision(t, r, false)
	storageTestExec(t, adapter, `BEGIN;
		INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES ('p','readers','data','read','allow');
		INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g2','alice','');
		INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES ('p2','bob','','read','allow');
		COMMIT`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
	err = r.Sync(context.Background())
	require.NoError(t, err)
	requireJournalDecision(t, r, true)
	found, err := r.model.HasPolicyEx("g", "g2", []string{"alice", ""})
	require.NoError(t, err)
	require.True(t, found)
	err = adapter.RemovePolicy("p", "p", []string{"readers", "data", "read", "allow"})
	require.NoError(t, err)
	err = r.Sync(context.Background())
	require.NoError(t, err)
	requireJournalDecision(t, r, false)
	revision := r.Status().Revision
	err = r.Sync(context.Background())
	require.NoError(t, err)
	require.Equal(t, revision, r.Status().Revision)
	restarted := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	require.Equal(t, revision, restarted.Status().Revision)
	requireJournalDecision(t, restarted, false)
	storageTestExec(t, adapter, "TRUNCATE ?.policies", bun.Name(adapter.matcher.SchemaName))
	err = r.Sync(context.Background())
	require.NoError(t, err)
	require.Empty(t, r.model["g"]["g2"].Policy)
	require.Empty(t, r.model["p"]["p2"].Policy)
}

func TestPostgresJournalReaderRejectsPartialAndInvalidState(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	r := newJournalTestReader(t, adapter, schema, JournalReaderOptions{BatchLimit: 1})
	err := adapter.AddPolicy("g", "g", []string{"alice", "readers"})
	require.NoError(t, err)
	err = adapter.AddPolicy("p", "p", []string{"readers", "data", "read", "allow"})
	require.NoError(t, err)
	err = r.Sync(context.Background())
	require.NoError(t, err)
	allowed, err := r.Enforce("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
	err = r.Sync(context.Background())
	require.NoError(t, err)
	requireJournalDecision(t, r, true)
	revision := r.Status().Revision
	storageTestExec(t, adapter, `BEGIN;
		DELETE FROM ?.policies WHERE ptype='p';
		INSERT INTO ?.policies (ptype,v0,v1) VALUES ('p','invalid','arity');
		COMMIT`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
	err = r.Sync(context.Background())
	require.Error(t, err)
	require.Equal(t, revision, r.Status().Revision)
	require.Len(t, r.model["p"]["p"].Policy, 1)
	allowed, err = r.Enforce("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
	storageTestExec(t, adapter, `DELETE FROM ?.policies WHERE ptype='p'`, bun.Name(adapter.matcher.SchemaName))
	err = r.Reload(context.Background())
	require.NoError(t, err)
	requireJournalDecision(t, r, false)
}

func TestPostgresJournalReaderConfigurationAndAtomicPublish(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES
		('p','alice','data','read','allow'), ('p','alice','data','read','deny')`, bun.Name(adapter.matcher.SchemaName))
	building := make(chan struct{}, 1)
	resume := make(chan struct{})
	var block atomic.Bool
	var fail atomic.Bool
	options := JournalReaderOptions{Configure: func(e *casbin.Enforcer) error {
		if block.Load() {
			building <- struct{}{}
			<-resume
		}
		if fail.Load() {
			return errors.New("configuration failed")
		}
		e.AddFunction("custom", func(args ...interface{}) (interface{}, error) { return true, nil })
		return nil
	}}
	m, err := model.NewModelFromString(strings.ReplaceAll(namedPolicyTestModel, "m = g(", "m = custom() && g("))
	require.NoError(t, err)
	r, err := adapter.NewJournalReader(context.Background(), schema, m, options)
	require.NoError(t, err)
	requireJournalDecision(t, r, false)
	storageTestExec(t, adapter, `BEGIN;
		DELETE FROM ?.policies WHERE v3='deny';
		INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES ('p','alice','data','read','deny');
		COMMIT`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
	block.Store(true)
	done := make(chan error, 1)
	go func() { done <- r.Sync(context.Background()) }()
	select {
	case <-building:
	case <-time.After(3 * time.Second):
		close(resume)
		t.Fatal("candidate build did not start")
	}
	for i := 0; i < 100; i++ {
		requireJournalDecision(t, r, false)
	}
	close(resume)
	require.NoError(t, <-done)
	block.Store(false)
	requireJournalDecision(t, r, false)
	revision := r.Status().Revision
	err = adapter.RemovePolicy("p", "p", []string{"alice", "data", "read", "deny"})
	require.NoError(t, err)
	fail.Store(true)
	err = r.Sync(context.Background())
	require.ErrorContains(t, err, "configuration failed")
	require.Equal(t, revision, r.Status().Revision)
	fail.Store(false)
	err = r.Sync(context.Background())
	require.NoError(t, err)
	requireJournalDecision(t, r, true)
}

type journalSnapshotHook struct {
	once    atomic.Bool
	started chan struct{}
	resume  chan struct{}
}

func (hook *journalSnapshotHook) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	return ctx
}

func (hook *journalSnapshotHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
	if strings.Contains(event.Query, "tableoid::bigint AS identity") && hook.once.CompareAndSwap(false, true) {
		close(hook.started)
		<-hook.resume
	}
}

type journalReaderTestResult struct {
	Reader *JournalReader
	Error  error
}

func TestPostgresJournalReaderInitialSnapshotHasNoGap(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	hook := &journalSnapshotHook{started: make(chan struct{}), resume: make(chan struct{})}
	adapter.AddQueryHook(hook)
	m, err := model.NewModelFromString(namedPolicyTestModel)
	require.NoError(t, err)
	done := make(chan journalReaderTestResult, 1)
	go func() {
		r, err := adapter.NewJournalReader(context.Background(), schema, m, JournalReaderOptions{})
		done <- journalReaderTestResult{Reader: r, Error: err}
	}()
	select {
	case <-hook.started:
	case <-time.After(3 * time.Second):
		close(hook.resume)
		t.Fatal("snapshot query did not start")
	}
	err = adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	close(hook.resume)
	require.NoError(t, err)
	result := <-done
	require.NoError(t, result.Error)
	require.Equal(t, int64(0), result.Reader.Status().Revision)
	requireJournalDecision(t, result.Reader, false)
	err = result.Reader.Sync(context.Background())
	require.NoError(t, err)
	requireJournalDecision(t, result.Reader, true)
}

func TestPostgresJournalReaderCancellationAndStaleness(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	r := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := r.Sync(ctx)
	require.ErrorIs(t, err, context.Canceled)
	allowed, err := r.Enforce("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
	err = r.Sync(context.Background())
	require.NoError(t, err)
	r.viewMu.Lock()
	r.status.LastSync = time.Now().Add(-time.Minute)
	r.viewMu.Unlock()
	allowed, err = r.Enforce("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
	err = r.Run(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestPostgresJournalCleanupAndRecovery(t *testing.T) {
	adapter := prepareStorageTest(t)
	role := prepareJournalTestRole(t, adapter)
	schema := prepareJournalTest(t, adapter, role)
	r := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	storageTestExec(t, adapter, "UPDATE ?.batches SET created_at = now() - interval '8 days'", bun.Name(schema))
	err = adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "deny"})
	require.NoError(t, err)
	err = execJournalTestAsRole(adapter, role, "SELECT ?.prune_changes()", bun.Name(schema))
	require.NoError(t, err)
	changes := readJournalTestChanges(t, adapter, schema)
	require.Len(t, changes, 1)
	require.Equal(t, int64(2), changes[0].Revision)
	err = r.Sync(context.Background())
	require.NoError(t, err)
	requireJournalDecision(t, r, false)
	require.Equal(t, int64(2), r.Status().Revision)
	storageTestExec(t, adapter, "UPDATE ?.batches SET created_at = now() - interval '8 days'", bun.Name(schema))
	removed, err := adapter.PruneJournal(context.Background(), schema)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	require.Empty(t, readJournalTestChanges(t, adapter, schema))
	restarted := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})
	requireJournalDecision(t, restarted, false)
	err = adapter.RemovePolicy("p", "p", []string{"alice", "data", "read", "deny"})
	require.NoError(t, err)
	err = restarted.Sync(context.Background())
	require.NoError(t, err)
	requireJournalDecision(t, restarted, true)
}

func TestPostgresJournalCleanupSkipsWriterAndKeepsPrefix(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	err := adapter.AddPolicy("g", "g", []string{"one", "readers"})
	require.NoError(t, err)
	err = adapter.AddPolicy("g", "g", []string{"two", "readers"})
	require.NoError(t, err)
	// A clock adjustment must not cause a hole behind a newer transaction.
	storageTestExec(t, adapter, "UPDATE ?.batches SET created_at=now()-interval '8 days' WHERE revision=2", bun.Name(schema))
	removed, err := adapter.PruneJournal(context.Background(), schema)
	require.NoError(t, err)
	require.Zero(t, removed)
	storageTestExec(t, adapter, "UPDATE ?.batches SET created_at=now()-interval '8 days'", bun.Name(schema))
	tx, err := adapter.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.Exec("INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','three','readers')", bun.Name(adapter.matcher.SchemaName))
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	removed, err = adapter.PruneJournal(ctx, schema)
	require.NoError(t, err)
	require.Zero(t, removed)
	err = tx.Commit()
	require.NoError(t, err)
	removed, err = adapter.PruneJournal(context.Background(), schema)
	require.NoError(t, err)
	require.Equal(t, 2, removed)
	require.Len(t, readJournalTestChanges(t, adapter, schema), 1)
}

func TestPostgresJournalReaderRunRecoversAndStops(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	r := newJournalTestReader(t, adapter, schema, JournalReaderOptions{PollInterval: 10 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	require.Eventually(t, func() bool {
		allowed, err := r.Enforce("alice", "data", "read")
		return err == nil && allowed
	}, 3*time.Second, 10*time.Millisecond)
	storageTestExec(t, adapter, "ALTER TABLE ?.state RENAME TO unavailable_state", bun.Name(schema))
	require.Eventually(t, func() bool { return r.Status().Error != nil }, 3*time.Second, 10*time.Millisecond)
	allowed, err := r.Enforce("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
	storageTestExec(t, adapter, "ALTER TABLE ?.unavailable_state RENAME TO state", bun.Name(schema))
	require.Eventually(t, func() bool {
		allowed, err := r.Enforce("alice", "data", "read")
		return err == nil && allowed
	}, 3*time.Second, 10*time.Millisecond)
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	allowed, err = r.Enforce("alice", "data", "read")
	require.False(t, allowed)
	require.ErrorIs(t, err, ErrJournalNotReady)
}

func TestPostgresJournalCleanupInstallationAndLimit(t *testing.T) {
	adapter := prepareStorageTest(t)
	role := prepareJournalTestRole(t, adapter)
	schema := prepareJournalTest(t, adapter, role)
	storageTestExec(t, adapter, "DROP FUNCTION ?.prune_changes()", bun.Name(schema))
	err := adapter.EnableJournalCleanup(context.Background(), schema, role)
	require.NoError(t, err)
	err = adapter.EnableJournalCleanup(context.Background(), schema, role)
	require.NoError(t, err)
	err = execJournalTestAsRole(adapter, role, "SELECT ?.prune_changes()", bun.Name(schema))
	require.NoError(t, err)
	// Seed historical batches as the owner to verify the cleanup limit independently of write throughput.
	storageTestExec(t, adapter, `INSERT INTO ?.batches (revision,transaction_id,created_at)
		SELECT n,pg_current_xact_id(),now()-interval '8 days' FROM generate_series(1,1005) n`, bun.Name(schema))
	storageTestExec(t, adapter, `INSERT INTO ?.changes (revision,operation)
		SELECT revision,'TRUNCATE' FROM ?.batches`, bun.Name(schema), bun.Name(schema))
	storageTestExec(t, adapter, "UPDATE ?.state SET revision=1005", bun.Name(schema))
	removed, err := adapter.PruneJournal(context.Background(), schema)
	require.NoError(t, err)
	require.Equal(t, 1000, removed)
	require.Len(t, readJournalTestChanges(t, adapter, schema), 5)
	removed, err = adapter.PruneJournal(context.Background(), schema)
	require.NoError(t, err)
	require.Equal(t, 5, removed)
}

func TestPostgresJournalReaderConditionalRoles(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	m, err := model.NewModelFromString(strings.Replace(namedPolicyTestModel, "g = _, _", "g = _, _, (_, _)", 1))
	require.NoError(t, err)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES
		('p','readers','data','read','allow'), ('g','alice','readers','start','end')`, bun.Name(adapter.matcher.SchemaName))
	r, err := adapter.NewJournalReader(context.Background(), schema, m, JournalReaderOptions{Configure: func(e *casbin.Enforcer) error {
		ok := e.AddNamedLinkConditionFunc("g", "alice", "readers", func(values ...string) (bool, error) { return false, nil })
		if !ok {
			return errors.New("missing conditional role link")
		}
		return nil
	}})
	require.NoError(t, err)
	requireJournalDecision(t, r, false)
	err = adapter.AddPolicy("p", "p", []string{"nobody", "data", "read", "allow"})
	require.NoError(t, err)
	err = r.Sync(context.Background())
	require.NoError(t, err)
	requireJournalDecision(t, r, false)
}
