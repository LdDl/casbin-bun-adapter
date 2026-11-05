package casbinbunadapter

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/casbin/casbin/v2/model"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

const journalLoadTestModel = `[request_definition]
r = sub, obj, act
[policy_definition]
p = sub, obj, act, eft
[role_definition]
g = _, _
[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))
[matchers]
m = r.obj == p.obj && r.act == p.act && g(r.sub, p.sub)
`

func prepareJournalLoadTest(t testing.TB, count int) (*BunAdapter, string) {
	t.Helper()
	adapter := prepareStorageTest(t)
	storageTestExec(t, adapter, `
		INSERT INTO ?.policies (ptype,v0,v1,v2,v3)
		SELECT 'p','role_' || n,'data_' || n,'read','allow' FROM generate_series(1, ?) n;
		INSERT INTO ?.policies (ptype,v0,v1)
		SELECT 'g','user_' || n,'role_' || n FROM generate_series(1, ?) n;
	`, bun.Name(adapter.matcher.SchemaName), count, bun.Name(adapter.matcher.SchemaName), count)
	return adapter, prepareJournalTest(t, adapter)
}

func newJournalLoadTestReader(t testing.TB, adapter *BunAdapter, schema string, options JournalReaderOptions) *JournalReader {
	t.Helper()
	policyModel, err := model.NewModelFromString(journalLoadTestModel)
	require.NoError(t, err)
	reader, err := adapter.NewJournalReader(context.Background(), schema, policyModel, options)
	require.NoError(t, err)
	return reader
}

func BenchmarkPostgresJournalStartup(b *testing.B) {
	for _, count := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			adapter, schema := prepareJournalLoadTest(b, count)
			runtime.GC()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			var reader *JournalReader
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				reader = newJournalLoadTestReader(b, adapter, schema, JournalReaderOptions{Timeout: time.Minute})
			}
			b.StopTimer()
			runtime.GC()
			runtime.ReadMemStats(&after)
			// Retained Go heap includes the canonical policies and both enforcers; PostgreSQL memory is separate.
			if after.HeapAlloc >= before.HeapAlloc {
				b.ReportMetric(float64(after.HeapAlloc-before.HeapAlloc), "live-B/reader")
			}
			runtime.KeepAlive(reader)
		})
	}
}

func BenchmarkPostgresJournalUnchangedCheck(b *testing.B) {
	for _, count := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			adapter, schema := prepareJournalLoadTest(b, count)
			reader := newJournalLoadTestReader(b, adapter, schema, JournalReaderOptions{Timeout: time.Minute})
			allowed, err := reader.Enforce("user_1", "data_1", "read")
			require.NoError(b, err)
			require.True(b, allowed)
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				allowed, err = reader.Enforce("user_1", "data_1", "read")
				require.NoError(b, err)
				require.True(b, allowed)
			}
		})
	}
}

func BenchmarkPostgresJournalSingleChange(b *testing.B) {
	for _, count := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			adapter, schema := prepareJournalLoadTest(b, count)
			reader := newJournalLoadTestReader(b, adapter, schema, JournalReaderOptions{Timeout: time.Minute})
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				b.StopTimer()
				// Rotate through the first remaining policy to include Casbin's removal reindexing cost.
				storageTestExec(b, adapter, `UPDATE ?.policies
					SET v3=CASE WHEN v3='allow' THEN 'deny' ELSE 'allow' END WHERE id=?`,
					bun.Name(adapter.matcher.SchemaName), index%count+1)
				b.StartTimer()
				err := reader.Sync(context.Background())
				require.NoError(b, err)
			}
		})
	}
}

func BenchmarkPostgresJournalBulkChange(b *testing.B) {
	for _, count := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			adapter, schema := prepareJournalLoadTest(b, count)
			reader := newJournalLoadTestReader(b, adapter, schema, JournalReaderOptions{})
			b.ReportAllocs()
			b.ResetTimer()
			for index := 0; index < b.N; index++ {
				b.StopTimer()
				storageTestExec(b, adapter, `UPDATE ?.policies
					SET v3=CASE WHEN v3='allow' THEN 'deny' ELSE 'allow' END WHERE ptype='p'`,
					bun.Name(adapter.matcher.SchemaName))
				b.StartTimer()
				err := reader.Sync(context.Background())
				require.NoError(b, err)
			}
		})
	}
}
