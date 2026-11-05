package casbinbunadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/casbin/casbin/v2/model"
	"github.com/stretchr/testify/require"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/driver/pgdriver"
)

type journalTestChange struct {
	ID        int64
	Revision  int64
	Operation string
	OldPolicy json.RawMessage
	NewPolicy json.RawMessage
}

func prepareJournalTest(t *testing.T, adapter *BunAdapter, readerRoles ...string) string {
	t.Helper()
	schema := adapter.matcher.SchemaName + "_journal"
	err := adapter.CreateJournal(context.Background(), schema, readerRoles...)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := adapter.Exec("DROP SCHEMA ? CASCADE", bun.Name(schema))
		require.NoError(t, err)
	})
	return schema
}

func readJournalTestChanges(t *testing.T, adapter *BunAdapter, schema string) []journalTestChange {
	t.Helper()
	var changes []journalTestChange
	err := adapter.NewSelect().
		TableExpr("?.changes", bun.Name(schema)).
		Column("id", "revision", "operation", "old_policy", "new_policy").
		Order("id").
		Scan(context.Background(), &changes)
	require.NoError(t, err)
	return changes
}

func requireJournalTestHead(t *testing.T, adapter *BunAdapter, schema string, expected int64) {
	t.Helper()
	var head int64
	err := adapter.NewRaw("SELECT revision FROM ?.state", bun.Name(schema)).Scan(context.Background(), &head)
	require.NoError(t, err)
	require.Equal(t, expected, head)
	var batches int64
	err = adapter.NewRaw("SELECT count(*) FROM ?.batches", bun.Name(schema)).Scan(context.Background(), &batches)
	require.NoError(t, err)
	require.Equal(t, expected, batches)
}

func TestPostgresJournalTransactionAndRollback(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	ctx := context.Background()
	policySchema := bun.Name(adapter.matcher.SchemaName)
	err := adapter.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g2','alice','')`, policySchema)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE ?.policies SET v1='readers'`, policySchema)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM ?.policies`, policySchema)
		return err
	})
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 1)
	changes := readJournalTestChanges(t, adapter, schema)
	require.Len(t, changes, 3)
	for _, change := range changes {
		require.Equal(t, int64(1), change.Revision)
	}
	require.Equal(t, "INSERT", changes[0].Operation)
	require.Equal(t, "UPDATE", changes[1].Operation)
	require.Equal(t, "DELETE", changes[2].Operation)
	require.JSONEq(t, `{"ptype":"g2","v0":"alice","v1":"","v2":null,"v3":null,"v4":null,"v5":null}`, string(changes[0].NewPolicy))
	require.JSONEq(t, string(changes[0].NewPolicy), string(changes[1].OldPolicy))
	require.JSONEq(t, string(changes[1].NewPolicy), string(changes[2].OldPolicy))

	abort := errors.New("abort transaction")
	err = adapter.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','bob','readers')`, policySchema)
		if err != nil {
			return err
		}
		return abort
	})
	require.ErrorIs(t, err, abort)
	requireJournalTestHead(t, adapter, schema, 1)
	require.Len(t, readJournalTestChanges(t, adapter, schema), 3)
	storageTestCount(t, adapter, 0)
	err = adapter.AddPolicy("g", "g", []string{"bob", "readers"})
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 2)
	changes = readJournalTestChanges(t, adapter, schema)
	require.Equal(t, int64(2), changes[3].Revision)
}

func TestPostgresJournalSaveAndNoOp(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	err := adapter.AddPolicy("g", "g", []string{"alice", "readers"})
	require.NoError(t, err)
	err = adapter.AddPolicy("g", "g", []string{"alice", "readers"})
	require.NoError(t, err)
	storageTestExec(t, adapter, `UPDATE ?.policies SET v0=v0`, bun.Name(adapter.matcher.SchemaName))
	storageTestExec(t, adapter, `DELETE FROM ?.policies WHERE false`, bun.Name(adapter.matcher.SchemaName))
	requireJournalTestHead(t, adapter, schema, 1)
	require.Len(t, readJournalTestChanges(t, adapter, schema), 1)

	m, err := model.NewModelFromString(storageTestModel)
	require.NoError(t, err)
	err = m.AddPolicy("p", "p", []string{"readers", "data", "read", "allow"})
	require.NoError(t, err)
	err = adapter.SavePolicy(m)
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 2)
	changes := readJournalTestChanges(t, adapter, schema)
	require.Len(t, changes, 3)
	require.Equal(t, "TRUNCATE", changes[1].Operation)
	require.Equal(t, "INSERT", changes[2].Operation)
	require.Equal(t, int64(2), changes[1].Revision)
	require.Equal(t, int64(2), changes[2].Revision)

	err = m.AddPolicy("p", "p", []string{strings.Repeat("x", 65), "data", "read", "allow"})
	require.NoError(t, err)
	err = adapter.SavePolicy(m)
	require.Error(t, err)
	requireJournalTestHead(t, adapter, schema, 2)
	require.Len(t, readJournalTestChanges(t, adapter, schema), 3)
	storageTestCount(t, adapter, 1)
}

func TestPostgresJournalWriterCommitOrder(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	first, err := adapter.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer first.Rollback()
	_, err = first.ExecContext(ctx, `INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','first','readers')`, bun.Name(adapter.matcher.SchemaName))
	require.NoError(t, err)

	second, err := adapter.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer second.Rollback()
	var firstPID, secondPID int
	err = first.NewRaw("SELECT pg_backend_pid()").Scan(ctx, &firstPID)
	require.NoError(t, err)
	err = second.NewRaw("SELECT pg_backend_pid()").Scan(ctx, &secondPID)
	require.NoError(t, err)
	result := make(chan error, 1)
	go func() {
		_, err := second.ExecContext(ctx, `INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','second','readers')`, bun.Name(adapter.matcher.SchemaName))
		result <- err
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		err = adapter.NewRaw("SELECT ? = ANY(pg_blocking_pids(?))", firstPID, secondPID).Scan(ctx, &blocked)
		require.NoError(t, err)
		if blocked {
			break
		}
		select {
		case err := <-result:
			t.Fatalf("second writer did not wait: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-ticker.C:
		}
	}
	// Uncommitted batches and changes are invisible to readers.
	requireJournalTestHead(t, adapter, schema, 0)
	require.Empty(t, readJournalTestChanges(t, adapter, schema))
	err = first.Commit()
	require.NoError(t, err)
	err = <-result
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 1)
	err = second.Commit()
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 2)
	changes := readJournalTestChanges(t, adapter, schema)
	require.Len(t, changes, 2)
	require.Equal(t, int64(1), changes[0].Revision)
	require.Equal(t, int64(2), changes[1].Revision)
}

func TestPostgresJournalSavepoint(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	err := adapter.RunInTx(context.Background(), nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.ExecContext(ctx, `
			SAVEPOINT before_changes;
			INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','discarded','readers');
			ROLLBACK TO SAVEPOINT before_changes;
			INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','kept','readers');
		`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
		return err
	})
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 1)
	changes := readJournalTestChanges(t, adapter, schema)
	require.Len(t, changes, 1)
	require.Contains(t, string(changes[0].NewPolicy), "kept")
}

func prepareJournalTestRole(t *testing.T, adapter *BunAdapter) string {
	t.Helper()
	var canCreateRole bool
	err := adapter.NewRaw("SELECT rolsuper OR rolcreaterole FROM pg_roles WHERE rolname = current_user").Scan(context.Background(), &canCreateRole)
	require.NoError(t, err)
	if !canCreateRole {
		t.Skip("role creation permission is required for journal privilege tests")
	}
	role := adapter.matcher.SchemaName + "_app"
	storageTestExec(t, adapter, "CREATE ROLE ? NOLOGIN", bun.Name(role))
	t.Cleanup(func() {
		storageTestExec(t, adapter, "DROP OWNED BY ?", bun.Name(role))
		storageTestExec(t, adapter, "DROP ROLE ?", bun.Name(role))
	})
	return role
}

func execJournalTestAsRole(adapter *BunAdapter, role, query string, args ...interface{}) error {
	return adapter.RunInTx(context.Background(), nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.ExecContext(ctx, "SET LOCAL ROLE ?", bun.Name(role))
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, query, args...)
		return err
	})
}

func TestPostgresJournalPrivileges(t *testing.T) {
	adapter := prepareStorageTest(t)
	role := prepareJournalTestRole(t, adapter)
	schema := prepareJournalTest(t, adapter, role)
	storageTestExec(t, adapter, "GRANT USAGE ON SCHEMA ? TO ?", bun.Name(adapter.matcher.SchemaName), bun.Name(role))
	storageTestExec(t, adapter, "GRANT SELECT, INSERT, UPDATE, DELETE, TRUNCATE ON ?.policies TO ?", bun.Name(adapter.matcher.SchemaName), bun.Name(role))
	err := execJournalTestAsRole(adapter, role, `INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','alice','readers')`, bun.Name(adapter.matcher.SchemaName))
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 1)
	err = execJournalTestAsRole(adapter, role, "SELECT * FROM ?.changes", bun.Name(schema))
	require.NoError(t, err)
	queries := []string{
		"INSERT INTO ?.changes (revision,operation) VALUES (1,'TRUNCATE')",
		"UPDATE ?.changes SET operation='TRUNCATE',new_policy=NULL",
		"DELETE FROM ?.changes",
		"TRUNCATE ?.changes",
		"UPDATE ?.state SET revision=100",
		"DELETE FROM ?.batches",
		"SELECT ?.record_change()",
		"SELECT ?.lock_writer()",
		"ALTER TABLE ?.changes DISABLE TRIGGER ALL",
	}
	for _, query := range queries {
		err = execJournalTestAsRole(adapter, role, query, bun.Name(schema))
		var pgErr pgdriver.Error
		require.ErrorAs(t, err, &pgErr, query)
		require.Equal(t, "42501", pgErr.Field('C'), query)
	}
	err = execJournalTestAsRole(adapter, role, `SELECT pg_notify('CASBIN_UPDATE_MESSAGE', '{"event_type":"EVENT_CASBIN_INSERT"}')`)
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 1)
	require.Len(t, readJournalTestChanges(t, adapter, schema), 1)
	err = execJournalTestAsRole(adapter, role, "TRUNCATE ?.policies", bun.Name(adapter.matcher.SchemaName))
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 2)

	// Even a mistakenly granted function cannot journal another table's changes.
	storageTestExec(t, adapter, "CREATE TABLE ?.forged (LIKE ?.policies)", bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.SchemaName))
	storageTestExec(t, adapter, "CREATE TRIGGER forged AFTER INSERT ON ?.forged FOR EACH ROW EXECUTE FUNCTION ?.record_change()", bun.Name(adapter.matcher.SchemaName), bun.Name(schema))
	_, err = adapter.Exec("INSERT INTO ?.forged (id,ptype,v0,v1) VALUES (1,'g','forged','admin')", bun.Name(adapter.matcher.SchemaName))
	require.ErrorContains(t, err, "Invalid policy journal trigger target")
	requireJournalTestHead(t, adapter, schema, 2)
}

func TestPostgresJournalRejectsDefaultGrants(t *testing.T) {
	adapter := prepareStorageTest(t)
	role := prepareJournalTestRole(t, adapter)
	storageTestExec(t, adapter, "ALTER DEFAULT PRIVILEGES GRANT SELECT ON TABLES TO ?", bun.Name(role))
	t.Cleanup(func() {
		storageTestExec(t, adapter, "ALTER DEFAULT PRIVILEGES REVOKE SELECT ON TABLES FROM ?", bun.Name(role))
	})
	schema := adapter.matcher.SchemaName + "_journal"
	err := adapter.CreateJournal(context.Background(), schema)
	require.ErrorContains(t, err, "default privileges")
	var absent bool
	err = adapter.NewRaw("SELECT NOT EXISTS (SELECT FROM pg_namespace WHERE nspname=?)", schema).Scan(context.Background(), &absent)
	require.NoError(t, err)
	require.True(t, absent)
}

func TestPostgresJournalQuotesAndInstallationRollback(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := adapter.matcher.SchemaName + `"?.$body$'`
	column := `subject"?.$body$'`
	storageTestExec(t, adapter, "ALTER TABLE ?.policies RENAME COLUMN v0 TO ?", bun.Name(adapter.matcher.SchemaName), bun.Name(column))
	adapter.matcher.V0 = column
	err := adapter.CreateJournal(context.Background(), schema)
	require.NoError(t, err)
	t.Cleanup(func() {
		storageTestExec(t, adapter, "DROP SCHEMA ? CASCADE", bun.Name(schema))
	})
	_, err = adapter.Exec("INSERT INTO ?.policies (ptype,?,v1) VALUES ('g2','alice','readers')", bun.Name(adapter.matcher.SchemaName), bun.Name(column))
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 1)
	changes := readJournalTestChanges(t, adapter, schema)
	require.Len(t, changes, 1)
	require.Contains(t, string(changes[0].NewPolicy), `"v0": "alice"`)
	err = adapter.CreateJournal(context.Background(), schema)
	require.Error(t, err)
	requireJournalTestHead(t, adapter, schema, 1)
	otherSchema := adapter.matcher.SchemaName + "_other"
	err = adapter.CreateJournal(context.Background(), otherSchema)
	require.Error(t, err)
	var absent bool
	err = adapter.NewRaw("SELECT NOT EXISTS (SELECT FROM pg_namespace WHERE nspname=?)", otherSchema).Scan(context.Background(), &absent)
	require.NoError(t, err)
	require.True(t, absent)
	_, err = adapter.Exec("INSERT INTO ?.policies (ptype,?,v1) VALUES ('g2','bob','readers')", bun.Name(adapter.matcher.SchemaName), bun.Name(column))
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 2)
}

func TestPostgresJournalFailureRollsBackPolicy(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	storageTestExec(t, adapter, "DELETE FROM ?.state", bun.Name(schema))
	err := adapter.AddPolicy("g", "g", []string{"alice", "readers"})
	require.ErrorContains(t, err, "Policy journal state is missing")
	storageTestCount(t, adapter, 0)
	require.Empty(t, readJournalTestChanges(t, adapter, schema))
}

func TestPostgresJournalRejectsStaleTransactionSnapshot(t *testing.T) {
	adapter := prepareStorageTest(t)
	schema := prepareJournalTest(t, adapter)
	ctx := context.Background()
	stale, err := adapter.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	require.NoError(t, err)
	defer stale.Rollback()
	var head int64
	err = stale.NewRaw("SELECT revision FROM ?.state", bun.Name(schema)).Scan(ctx, &head)
	require.NoError(t, err)
	require.Zero(t, head)
	err = adapter.AddPolicy("g", "g", []string{"first", "readers"})
	require.NoError(t, err)
	_, err = stale.ExecContext(ctx, "INSERT INTO ?.policies (ptype,v0,v1) VALUES ('g','stale','readers')", bun.Name(adapter.matcher.SchemaName))
	var pgErr pgdriver.Error
	require.ErrorAs(t, err, &pgErr)
	require.Equal(t, "40001", pgErr.Field('C'))
	err = stale.Rollback()
	require.NoError(t, err)
	requireJournalTestHead(t, adapter, schema, 1)
	storageTestCount(t, adapter, 1)
	require.Len(t, readJournalTestChanges(t, adapter, schema), 1)
}
