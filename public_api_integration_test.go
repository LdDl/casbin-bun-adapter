package casbinbunadapter

import (
	"context"
	"testing"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

func TestPostgresJournalWithCasbinWriter(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	policyModel, err := model.NewModelFromString(namedPolicyTestModel)
	require.NoError(t, err)
	writer, err := casbin.NewSyncedEnforcer(policyModel, adapter)
	require.NoError(t, err)
	writer.EnableAutoSave(true)
	reader := newJournalTestReader(t, adapter, schema, JournalReaderOptions{})

	changed, err := writer.AddGroupingPolicy("alice", "readers")
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = writer.AddPolicy("readers", "data", "read", "allow")
	require.NoError(t, err)
	require.True(t, changed)
	requireJournalStrictDecision(t, reader, true)

	storageTestExec(t, adapter, "UPDATE ?.policies SET v3='deny' WHERE ptype='p'", bun.Name(adapter.matcher.SchemaName))
	requireJournalStrictDecision(t, reader, false)
	err = writer.LoadPolicy()
	require.NoError(t, err)
	allowed, err := writer.Enforce("alice", "data", "read")
	require.NoError(t, err)
	require.False(t, allowed)

	writer.EnableAutoSave(false)
	changed, err = writer.RemovePolicy("readers", "data", "read", "deny")
	require.NoError(t, err)
	require.True(t, changed)
	changed, err = writer.AddPolicy("readers", "data", "read", "allow")
	require.NoError(t, err)
	require.True(t, changed)
	requireJournalStrictDecision(t, reader, false)
	revision := reader.Status().Revision
	err = writer.SavePolicy()
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, true)
	require.Equal(t, revision+1, reader.Status().Revision)

	writer.EnableAutoSave(true)
	changed, err = writer.RemoveFilteredGroupingPolicy(0, "alice")
	require.NoError(t, err)
	require.True(t, changed)
	requireJournalStrictDecision(t, reader, false)
	err = reader.Reload(context.Background())
	require.NoError(t, err)
	requireJournalStrictDecision(t, reader, false)
}
