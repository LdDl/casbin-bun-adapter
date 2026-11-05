package casbinbunadapter

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/uptrace/bun"
)

type journalSource struct {
	OID            int64
	Kind           string
	Persistence    string
	Partition      bool
	Inherited      bool
	OwnerOID       int64
	SchemaOwnerOID int64
}

// CreateJournal installs a transactional change journal in a new, dedicated schema.
// It requires the policy table owner or a migration role with equivalent privileges.
// Optional reader roles receive schema usage and SELECT on the journal tables.
// Journal creation does not start synchronization with an enforcer.
func (adapter *BunAdapter) CreateJournal(ctx context.Context, schemaName string, readerRoles ...string) error {
	names := []string{schemaName, adapter.matcher.SchemaName, adapter.matcher.TableName, adapter.matcher.PType,
		adapter.matcher.V0, adapter.matcher.V1, adapter.matcher.V2, adapter.matcher.V3, adapter.matcher.V4, adapter.matcher.V5}
	for _, name := range names {
		err := validateJournalIdentifier(name)
		if err != nil {
			return err
		}
	}
	for _, role := range readerRoles {
		err := validateJournalIdentifier(role)
		if err != nil {
			return fmt.Errorf("invalid journal reader role: %w", err)
		}
	}
	columns := names[3:]
	seen := make(map[string]bool, len(columns))
	for _, column := range columns {
		if seen[column] {
			return fmt.Errorf("duplicate policy column: %q", column)
		}
		seen[column] = true
	}

	err := adapter.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.ExecContext(ctx, "LOCK TABLE ?.? IN ACCESS EXCLUSIVE MODE", bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName))
		if err != nil {
			return err
		}
		var source journalSource
		err = tx.NewRaw(`
			SELECT c.oid, c.relowner AS owner_oid, n.nspowner AS schema_owner_oid,
				c.relkind AS kind, c.relpersistence AS persistence, c.relispartition AS partition,
				EXISTS (SELECT FROM pg_catalog.pg_inherits i WHERE i.inhrelid = c.oid OR i.inhparent = c.oid) AS inherited
			FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = ? AND c.relname = ?
		`, adapter.matcher.SchemaName, adapter.matcher.TableName).Scan(ctx, &source)
		if err != nil {
			return err
		}
		if source.Kind != "r" || source.Persistence != "p" || source.Partition || source.Inherited {
			return fmt.Errorf("journal requires a permanent policy table without inheritance or partitioning")
		}
		var count int
		err = tx.NewRaw(`
			SELECT count(*) FROM pg_catalog.pg_attribute
			WHERE attrelid = ?::oid AND attname IN (?) AND NOT attisdropped
				AND atttypid IN ('pg_catalog.text'::regtype, 'pg_catalog.varchar'::regtype)
		`, source.OID, bun.In(columns)).Scan(ctx, &count)
		if err != nil {
			return err
		}
		if count != len(columns) {
			return fmt.Errorf("journal policy columns must exist and have text or varchar types")
		}

		// A new schema prevents reuse of objects owned or modified by another role.
		_, err = tx.ExecContext(ctx, "CREATE SCHEMA ?", bun.Name(schemaName))
		if err != nil {
			return err
		}
		journal := adapter.Formatter().FormatQuery("?", bun.Name(schemaName))
		_, err = tx.ExecContext(ctx, fmt.Sprintf(journalTablesSQL, journal))
		if err != nil {
			return err
		}
		lockBody := fmt.Sprintf(journalLockSQL, source.OID, journal)
		_, err = tx.ExecContext(ctx, `CREATE FUNCTION ?.lock_writer() RETURNS trigger
			LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS ?`, bun.Name(schemaName), lockBody)
		if err != nil {
			return err
		}
		oldFields := make([]string, len(columns))
		newFields := make([]string, len(columns))
		keys := []string{"ptype", "v0", "v1", "v2", "v3", "v4", "v5"}
		for i, column := range columns {
			oldFields[i] = adapter.Formatter().FormatQuery("?, OLD.?", keys[i], bun.Name(column))
			newFields[i] = adapter.Formatter().FormatQuery("?, NEW.?", keys[i], bun.Name(column))
		}
		recordBody := fmt.Sprintf(journalRecordSQL, source.OID, journal, strings.Join(oldFields, ", "), strings.Join(newFields, ", "))
		_, err = tx.ExecContext(ctx, `CREATE FUNCTION ?.record_change() RETURNS trigger
			LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS ?`, bun.Name(schemaName), recordBody)
		if err != nil {
			return err
		}
		err = adapter.createJournalCleanup(ctx, tx, schemaName, readerRoles)
		if err != nil {
			return err
		}
		err = adapter.createJournalNotifications(ctx, tx, schemaName)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, fmt.Sprintf(journalPermissionsSQL, journal))
		if err != nil {
			return err
		}
		// Default privileges may grant access to roles other than PUBLIC.
		var unexpectedGrants bool
		err = tx.NewRaw(journalGrantsSQL, schemaName, schemaName, schemaName).Scan(ctx, &unexpectedGrants)
		if err != nil {
			return err
		}
		if unexpectedGrants {
			return fmt.Errorf("journal default privileges grant access to non-owner roles; use a dedicated migration role without these defaults")
		}
		_, err = tx.ExecContext(ctx, `
			CREATE TRIGGER casbin_journal_lock
				BEFORE INSERT OR UPDATE OR DELETE OR TRUNCATE ON ?.?
				FOR EACH STATEMENT EXECUTE FUNCTION ?.lock_writer();
			CREATE TRIGGER casbin_journal_change
				AFTER INSERT OR UPDATE OR DELETE ON ?.?
				FOR EACH ROW EXECUTE FUNCTION ?.record_change();
			CREATE TRIGGER casbin_journal_truncate
				AFTER TRUNCATE ON ?.?
				FOR EACH STATEMENT EXECUTE FUNCTION ?.record_change();
			ALTER TABLE ?.? ENABLE ALWAYS TRIGGER casbin_journal_lock;
			ALTER TABLE ?.? ENABLE ALWAYS TRIGGER casbin_journal_change;
			ALTER TABLE ?.? ENABLE ALWAYS TRIGGER casbin_journal_truncate;
		`, bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName), bun.Name(schemaName),
			bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName), bun.Name(schemaName),
			bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName), bun.Name(schemaName),
			bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName),
			bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName),
			bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName))
		if err != nil {
			return err
		}
		return grantJournalReaders(ctx, tx, schemaName, source, readerRoles)
	})
	if err != nil {
		return fmt.Errorf("create policy journal: %w", err)
	}
	return nil
}

func validateJournalIdentifier(name string) error {
	if name == "" || len(name) > 63 || !utf8.ValidString(name) || strings.ContainsRune(name, '\x00') {
		return fmt.Errorf("invalid PostgreSQL identifier: %q", name)
	}
	return nil
}
