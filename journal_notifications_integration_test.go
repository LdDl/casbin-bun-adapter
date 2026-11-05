package casbinbunadapter

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

type journalRunTestWorker struct {
	cancel context.CancelFunc
	done   chan error
	once   sync.Once
	err    error
}

func startJournalRunTest(t *testing.T, reader *JournalReader) *journalRunTestWorker {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	worker := &journalRunTestWorker{cancel: cancel, done: make(chan error, 1)}
	initialSync := reader.Status().LastSync
	go func() { worker.done <- reader.Run(ctx) }()
	t.Cleanup(func() { worker.stop(t) })
	require.Eventually(t, func() bool {
		status := reader.Status()
		return status.Listening && status.LastSync.After(initialSync)
	}, 3*time.Second, time.Millisecond)
	return worker
}

func (worker *journalRunTestWorker) stop(t *testing.T) {
	t.Helper()
	worker.once.Do(func() {
		worker.cancel()
		select {
		case worker.err = <-worker.done:
		case <-time.After(3 * time.Second):
			t.Error("journal worker did not stop")
		}
	})
	require.ErrorIs(t, worker.err, context.Canceled)
}

func journalNamedTestAdapter(t *testing.T, source *BunAdapter) *BunAdapter {
	t.Helper()
	connector := pgdriver.NewConnector(
		pgdriver.WithDSN(os.Getenv("CASBIN_TEST_DSN")),
		pgdriver.WithApplicationName(source.matcher.SchemaName),
	)
	db := bun.NewDB(sql.OpenDB(connector), pgdialect.New())
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	return NewBunAdapter(db, WithMatcherOptions(source.matcher))
}

func requireJournalCaughtUp(t *testing.T, reader *JournalReader, revision int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		status := reader.Status()
		return status.Revision == revision && status.Error == nil
	}, 4*time.Second, time.Millisecond)
}

func TestPostgresJournalNotificationsCatchUpAndStop(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	listening := journalNamedTestAdapter(t, adapter)
	reader := newJournalTestReader(t, listening, schema, JournalReaderOptions{Listen: true, BatchLimit: 1})
	// This commit happens before LISTEN and cannot be recovered from notifications alone.
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	worker := startJournalRunTest(t, reader)
	requireJournalCaughtUp(t, reader, 1)
	requireJournalCachedDecision(t, reader, true)
	err = reader.Run(context.Background())
	require.ErrorContains(t, err, "already running")
	for index := 0; index < 12; index++ {
		err = adapter.AddPolicy("g", "g2", []string{time.Duration(index).String(), "readers"})
		require.NoError(t, err)
	}
	err = adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "deny"})
	require.NoError(t, err)
	requireJournalCaughtUp(t, reader, 14)
	requireJournalCachedDecision(t, reader, false)
	require.Zero(t, reader.options.PollInterval)
	worker.stop(t)
	require.False(t, reader.Status().Listening)
	var listeners int
	err = adapter.NewRaw(`
		SELECT count(*) FROM pg_stat_activity WHERE application_name = ? AND query LIKE 'LISTEN %'
	`, adapter.matcher.SchemaName).Scan(context.Background(), &listeners)
	require.NoError(t, err)
	require.Zero(t, listeners)
	err = adapter.RemovePolicy("p", "p", []string{"alice", "data", "read", "deny"})
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, true)
}

func TestPostgresJournalNotificationsReconnectWithoutAnotherEvent(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	listening := journalNamedTestAdapter(t, adapter)
	reader := newJournalTestReader(t, listening, schema, JournalReaderOptions{Listen: true, BatchLimit: 1})
	startJournalRunTest(t, reader)
	var pid int
	err := adapter.NewRaw(`
		SELECT pid FROM pg_stat_activity WHERE application_name = ? AND query LIKE 'LISTEN %'
	`, adapter.matcher.SchemaName).Scan(context.Background(), &pid)
	require.NoError(t, err)
	var terminated bool
	err = adapter.NewRaw("SELECT pg_terminate_backend(?)", pid).Scan(context.Background(), &terminated)
	require.NoError(t, err)
	require.True(t, terminated)
	require.Eventually(t, func() bool { return reader.Status().ListenError != nil }, time.Second, time.Millisecond)
	// Suppress wake-ups to verify that reconnecting itself closes the delivery gap.
	storageTestExec(t, adapter, "ALTER TABLE ?.batches DISABLE TRIGGER casbin_journal_notify", bun.Name(schema))
	err = adapter.AddPolicy("g", "g", []string{"alice", "readers"})
	require.NoError(t, err)
	err = adapter.AddPolicy("p", "p", []string{"readers", "data", "read", "allow"})
	require.NoError(t, err)
	err = adapter.AddPolicy("p", "p", []string{"readers", "data", "read", "deny"})
	require.NoError(t, err)
	storageTestExec(t, adapter, "ALTER TABLE ?.batches ENABLE ALWAYS TRIGGER casbin_journal_notify", bun.Name(schema))
	requireJournalCaughtUp(t, reader, 3)
	requireJournalCachedDecision(t, reader, false)
	require.True(t, reader.Status().Listening)
	require.NoError(t, reader.Status().ListenError)
}

func TestPostgresJournalNotificationPayloadHasNoAuthority(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{Listen: true})
	startJournalRunTest(t, reader)
	hook := &journalQueryCounterHook{}
	adapter.AddQueryHook(hook)
	// A notification from any database role may only request an authoritative journal read.
	err := pgdriver.Notify(context.Background(), adapter.DB, journalNotificationChannel(schema),
		`{"revision":999999,"ptype":"p","v0":"alice","v1":"data","v2":"read","v3":"allow"}`)
	require.NoError(t, err)
	require.Eventually(t, func() bool { return hook.Heads.Load() > 0 }, time.Second, time.Millisecond)
	requireJournalCachedDecision(t, reader, false)
	require.Zero(t, reader.Status().Revision)
	requireJournalTestHead(t, adapter, schema, 0)
}

type journalFailSyncTestHook struct {
	Fail atomic.Bool
}

func (hook *journalFailSyncTestHook) BeforeQuery(ctx context.Context, event *bun.QueryEvent) context.Context {
	if strings.Contains(event.Query, "tableoid::bigint AS identity") && hook.Fail.CompareAndSwap(true, false) {
		canceled, cancel := context.WithCancel(ctx)
		cancel()
		return canceled
	}
	return ctx
}

func (hook *journalFailSyncTestHook) AfterQuery(ctx context.Context, event *bun.QueryEvent) {
}

func TestPostgresJournalNotificationsRetryFailedSync(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{Listen: true})
	hook := &journalFailSyncTestHook{}
	adapter.AddQueryHook(hook)
	startJournalRunTest(t, reader)
	hook.Fail.Store(true)
	err := adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	require.Eventually(t, func() bool { return reader.Status().Error != nil }, time.Second, time.Millisecond)
	requireJournalCaughtUp(t, reader, 1)
	requireJournalCachedDecision(t, reader, true)
}

func TestPostgresJournalNotificationUpgrade(t *testing.T) {
	adapter := prepareStorageTest(t)
	role := prepareJournalTestRole(t, adapter)
	schema := prepareJournalTest(t, adapter, role)
	err := adapter.AddPolicy("g", "g", []string{"alice", "readers"})
	require.NoError(t, err)
	storageTestExec(t, adapter, "DROP TRIGGER casbin_journal_notify ON ?.batches", bun.Name(schema))
	storageTestExec(t, adapter, "DROP FUNCTION ?.notify_change()", bun.Name(schema))
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{Listen: true})
	err = reader.Run(context.Background())
	require.ErrorContains(t, err, "EnableJournalNotifications")
	for index := 0; index < 2; index++ {
		err = adapter.EnableJournalNotifications(context.Background(), schema)
		require.NoError(t, err)
	}
	requireJournalTestHead(t, adapter, schema, 1)
	err = execJournalTestAsRole(adapter, role, "SELECT ?.notify_change()", bun.Name(schema))
	require.ErrorContains(t, err, "permission denied")
	startJournalRunTest(t, reader)
	err = adapter.AddPolicy("p", "p", []string{"readers", "data", "read", "allow"})
	require.NoError(t, err)
	requireJournalCaughtUp(t, reader, 2)
	requireJournalCachedDecision(t, reader, true)
}

func TestPostgresJournalNotificationsUseApplicationRole(t *testing.T) {
	adapter := prepareStorageTest(t)
	role := prepareJournalTestRole(t, adapter)
	schema := prepareJournalTest(t, adapter, role)
	storageTestExec(t, adapter, "GRANT USAGE ON SCHEMA ? TO ?", bun.Name(adapter.matcher.SchemaName), bun.Name(role))
	storageTestExec(t, adapter, "GRANT SELECT ON ?.policies TO ?", bun.Name(adapter.matcher.SchemaName), bun.Name(role))
	connector := pgdriver.NewConnector(
		pgdriver.WithDSN(os.Getenv("CASBIN_TEST_DSN")),
		pgdriver.WithConnParams(map[string]interface{}{"role": role}),
	)
	db := bun.NewDB(sql.OpenDB(connector), pgdialect.New())
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	application := NewBunAdapter(db, WithMatcherOptions(adapter.matcher))
	var currentRole string
	err := application.NewRaw("SELECT current_user").Scan(context.Background(), &currentRole)
	require.NoError(t, err)
	require.Equal(t, role, currentRole)
	err = application.EnableJournalNotifications(context.Background(), schema)
	require.ErrorContains(t, err, "requires the journal owner")
	reader := newJournalTestReader(t, application, schema, JournalReaderOptions{Listen: true})
	startJournalRunTest(t, reader)
	err = adapter.AddPolicy("p", "p", []string{"alice", "data", "read", "allow"})
	require.NoError(t, err)
	requireJournalCaughtUp(t, reader, 1)
	requireJournalCachedDecision(t, reader, true)
	require.NoError(t, reader.Status().CleanupError)
}

func TestPostgresJournalNotificationsRespectTransaction(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	listener := pgdriver.NewListener(adapter.DB)
	t.Cleanup(func() { _ = listener.Close() })
	channel := journalNotificationChannel(schema)
	err := listener.Listen(context.Background(), channel)
	require.NoError(t, err)
	messages := listener.Channel(pgdriver.WithChannelSize(10))
	// Establish LISTEN before testing commit, rollback and coalescing behavior.
	ready := false
	deadline := time.Now().Add(time.Second)
	for !ready && time.Now().Before(deadline) {
		err = pgdriver.Notify(context.Background(), adapter.DB, channel, "ready")
		require.NoError(t, err)
		select {
		case message := <-messages:
			ready = message.Payload == "ready"
		case <-time.After(10 * time.Millisecond):
		}
	}
	require.True(t, ready)
	tx, err := adapter.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer tx.Rollback()
	_, err = tx.ExecContext(context.Background(), `INSERT INTO ?.policies (ptype,v0,v1)
		VALUES ('g','alice','readers'), ('g','bob','readers')`, bun.Name(adapter.matcher.SchemaName))
	require.NoError(t, err)
	message, found := readJournalTestNotification(messages, 20*time.Millisecond)
	require.False(t, found, "notification before commit: %+v", message)
	err = tx.Commit()
	require.NoError(t, err)
	message, found = readJournalTestNotification(messages, time.Second)
	require.True(t, found, "missing commit notification")
	require.Equal(t, channel, message.Channel)
	require.Empty(t, message.Payload)
	abort := errors.New("rollback")
	err = adapter.RunInTx(context.Background(), nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.ExecContext(ctx, "DELETE FROM ?.policies", bun.Name(adapter.matcher.SchemaName))
		if err != nil {
			return err
		}
		return abort
	})
	require.ErrorIs(t, err, abort)
	storageTestExec(t, adapter, "UPDATE ?.policies SET v0=v0", bun.Name(adapter.matcher.SchemaName))
	message, found = readJournalTestNotification(messages, 30*time.Millisecond)
	require.False(t, found, "unexpected extra notification: %+v", message)
	storageTestExec(t, adapter, "TRUNCATE ?.policies", bun.Name(adapter.matcher.SchemaName))
	message, found = readJournalTestNotification(messages, time.Second)
	require.True(t, found, "missing truncate notification")
	require.Empty(t, message.Payload)
}

func readJournalTestNotification(messages <-chan pgdriver.Notification, timeout time.Duration) (pgdriver.Notification, bool) {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		select {
		case message := <-messages:
			if message.Payload != "ready" {
				return message, true
			}
		case <-timer.C:
			return pgdriver.Notification{}, false
		}
	}
}
