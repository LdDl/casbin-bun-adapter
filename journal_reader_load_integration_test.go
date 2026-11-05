package casbinbunadapter

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

type journalBulkTestCase struct {
	Name    string
	Changes int
}

func TestPostgresJournalBulkRevocation(t *testing.T) {
	cases := []journalBulkTestCase{
		{Name: "large transaction", Changes: 10000},
		{Name: "reindexing a large model", Changes: 100},
	}
	for _, testCase := range cases {
		t.Run(testCase.Name, func(t *testing.T) {
			adapter, schema := prepareJournalLoadTest(t, 10000)
			reader := newJournalLoadTestReader(t, adapter, schema, JournalReaderOptions{Timeout: time.Minute})
			original := reader.model["p"]["p"]
			storageTestExec(t, adapter, `UPDATE ?.policies SET v3='deny' WHERE id<=?`,
				bun.Name(adapter.matcher.SchemaName), testCase.Changes)
			err := reader.Sync(context.Background())
			require.NoError(t, err)
			require.NotSame(t, original, reader.model["p"]["p"])
			require.EqualValues(t, 1, reader.Status().Revision)
			require.Len(t, reader.model["p"]["p"].Policy, 10000)
			denied := 0
			for _, rule := range reader.model["p"]["p"].Policy {
				if rule[3] == "deny" {
					denied++
				}
			}
			require.Equal(t, testCase.Changes, denied)
			for _, index := range []int{1, testCase.Changes, 10000} {
				allowed, err := reader.Enforce(fmt.Sprint("user_", index), fmt.Sprint("data_", index), "read")
				require.NoError(t, err)
				require.Equal(t, index > testCase.Changes, allowed)
			}
		})
	}
}

func TestPostgresJournalBulkSnapshotRejectsInvalidPolicies(t *testing.T) {
	adapter, schema := prepareJournalLoadTest(t, 2000)
	reader := newJournalLoadTestReader(t, adapter, schema, JournalReaderOptions{})
	original := reader.view
	storageTestExec(t, adapter, `BEGIN;
		UPDATE ?.policies SET v3='deny' WHERE ptype='p';
		INSERT INTO ?.policies (ptype,v0,v1) VALUES ('p','invalid','arity');
		COMMIT`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
	allowed, err := reader.Enforce("user_1", "data_1", "read")
	require.ErrorIs(t, err, ErrJournalNotReady)
	require.False(t, allowed)
	require.Same(t, original, reader.view)
	require.Zero(t, reader.Status().Revision)
	storageTestExec(t, adapter, `DELETE FROM ?.policies WHERE v0='invalid'`, bun.Name(adapter.matcher.SchemaName))
	allowed, err = reader.Enforce("user_1", "data_1", "read")
	require.NoError(t, err)
	require.False(t, allowed)
	require.EqualValues(t, 2, reader.Status().Revision)
}

func TestPostgresJournalBulkSnapshotUsesOneDatabaseState(t *testing.T) {
	adapter, schema := prepareJournalLoadTest(t, 2000)
	reader := newJournalLoadTestReader(t, adapter, schema, JournalReaderOptions{})
	storageTestExec(t, adapter, `UPDATE ?.policies SET v3='deny' WHERE ptype='p'`, bun.Name(adapter.matcher.SchemaName))
	hook := &journalSnapshotHook{started: make(chan struct{}), resume: make(chan struct{})}
	resume := sync.OnceFunc(func() { close(hook.resume) })
	t.Cleanup(resume)
	adapter.AddQueryHook(hook)
	done := make(chan error, 1)
	go func() { done <- reader.Sync(context.Background()) }()
	select {
	case <-hook.started:
	case <-time.After(3 * time.Second):
		t.Fatal("snapshot head query did not start")
	}
	storageTestExec(t, adapter, `UPDATE ?.policies SET v3='allow' WHERE ptype='p'`, bun.Name(adapter.matcher.SchemaName))
	resume()
	require.NoError(t, <-done)
	require.EqualValues(t, 1, reader.Status().Revision)
	for _, rule := range reader.model["p"]["p"].Policy {
		require.Equal(t, "deny", rule[3])
	}
	allowed, err := reader.Enforce("user_2000", "data_2000", "read")
	require.NoError(t, err)
	require.True(t, allowed)
	require.EqualValues(t, 2, reader.Status().Revision)
}

func TestPostgresJournalMultipleReadersAndPrunedHistory(t *testing.T) {
	adapter, schema := prepareJournalLoadTest(t, 1000)
	var readers []*JournalReader
	for index := 0; index < 4; index++ {
		connection := journalNamedTestAdapter(t, adapter)
		reader := newJournalLoadTestReader(t, connection, schema, JournalReaderOptions{Listen: index < 2, BatchLimit: 3})
		readers = append(readers, reader)
		if index < 2 {
			startJournalRunTest(t, reader)
		}
	}
	for round := 0; round < 12; round++ {
		allowed := round%2 != 0
		effect := "deny"
		if allowed {
			effect = "allow"
		}
		storageTestExec(t, adapter, `UPDATE ?.policies SET v3=? WHERE id<=24`, bun.Name(adapter.matcher.SchemaName), effect)
		// Every request below starts after commit; the fourth reader deliberately stays offline.
		results := make(chan error, 3)
		for _, reader := range readers[:3] {
			go func(reader *JournalReader) {
				for index := 1; index <= 24; index++ {
					actual, err := reader.Enforce(fmt.Sprint("user_", index), fmt.Sprint("data_", index), "read")
					if err != nil {
						results <- err
						return
					}
					if actual != allowed {
						results <- fmt.Errorf("reader allowed=%v for user_%d, want %v", actual, index, allowed)
						return
					}
				}
				results <- nil
			}(reader)
		}
		for index := 0; index < 3; index++ {
			err := <-results
			require.NoError(t, err)
		}
	}
	require.Zero(t, readers[3].Status().Revision)
	storageTestExec(t, adapter, `UPDATE ?.batches SET created_at=now()-interval '8 days'`, bun.Name(schema))
	removed, err := adapter.PruneJournal(context.Background(), schema)
	require.NoError(t, err)
	require.Equal(t, 12, removed)
	storageTestExec(t, adapter, `UPDATE ?.policies SET v3='deny' WHERE id<=24`, bun.Name(adapter.matcher.SchemaName))
	for _, reader := range readers {
		allowed, err := reader.Enforce("user_24", "data_24", "read")
		require.NoError(t, err)
		require.False(t, allowed)
		require.EqualValues(t, 13, reader.Status().Revision)
	}
	restarted := newJournalLoadTestReader(t, journalNamedTestAdapter(t, adapter), schema, JournalReaderOptions{})
	allowed, err := restarted.Enforce("user_24", "data_24", "read")
	require.NoError(t, err)
	require.False(t, allowed)
	require.EqualValues(t, 13, restarted.Status().Revision)
}
