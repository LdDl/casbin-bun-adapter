package casbinbunadapter

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
)

func TestPostgresJournalWithoutReaderGrants(t *testing.T) {
	adapter := prepareStorageTest(t)
	role := prepareJournalTestRole(t, adapter)
	schema := prepareJournalTest(t, adapter)
	err := execJournalTestAsRole(adapter, role, "SELECT * FROM ?.changes", bun.Name(schema))
	require.Error(t, err)
}

func TestPostgresJournalReaderInstallationRollback(t *testing.T) {
	adapter := prepareStorageTest(t)
	role := prepareJournalTestRole(t, adapter)
	schema := adapter.matcher.SchemaName + "_journal"
	err := adapter.CreateJournal(context.Background(), schema, role, role+"_missing")
	require.ErrorContains(t, err, "does not exist")
	var absent bool
	err = adapter.NewRaw("SELECT NOT EXISTS (SELECT FROM pg_namespace WHERE nspname=?)", schema).Scan(context.Background(), &absent)
	require.NoError(t, err)
	require.True(t, absent)
	schema = prepareJournalTest(t, adapter, role)
	for _, table := range []string{"state", "batches", "changes"} {
		err = execJournalTestAsRole(adapter, role, "SELECT * FROM ?.?", bun.Name(schema), bun.Name(table))
		require.NoError(t, err)
	}
}

func TestPostgresJournalRejectsOwnerReader(t *testing.T) {
	adapter := prepareStorageTest(t)
	var owner string
	err := adapter.NewRaw("SELECT current_user").Scan(context.Background(), &owner)
	require.NoError(t, err)
	err = adapter.CreateJournal(context.Background(), adapter.matcher.SchemaName+"_journal", owner)
	require.ErrorContains(t, err, "owner or administrative privileges")
}

func TestPostgresJournalRejectsPrivilegedReaders(t *testing.T) {
	for _, capability := range []string{"createrole", "owner_membership", "global_write"} {
		t.Run(capability, func(t *testing.T) {
			adapter := prepareStorageTest(t)
			role := prepareJournalTestRole(t, adapter)
			// NOINHERIT still allows assuming roles on supported PostgreSQL versions.
			storageTestExec(t, adapter, "ALTER ROLE ? NOINHERIT", bun.Name(role))
			switch capability {
			case "createrole":
				storageTestExec(t, adapter, "ALTER ROLE ? CREATEROLE", bun.Name(role))
			case "owner_membership":
				var owner string
				err := adapter.NewRaw("SELECT current_user").Scan(context.Background(), &owner)
				require.NoError(t, err)
				storageTestExec(t, adapter, "GRANT ? TO ?", bun.Name(owner), bun.Name(role))
			case "global_write":
				storageTestExec(t, adapter, "GRANT pg_write_all_data TO ?", bun.Name(role))
			}
			err := adapter.CreateJournal(context.Background(), adapter.matcher.SchemaName+"_journal", role)
			require.ErrorContains(t, err, "owner or administrative privileges")
		})
	}
}
