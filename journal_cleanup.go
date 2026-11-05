package casbinbunadapter

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

const journalCleanupBatchLimit = 1000

// EnableJournalCleanup installs bounded cleanup for an existing journal as its owner.
// Reader roles can invoke cleanup but cannot choose its retention period or delete arbitrary rows.
func (adapter *BunAdapter) EnableJournalCleanup(ctx context.Context, schemaName string, readerRoles ...string) error {
	err := validateJournalIdentifier(schemaName)
	if err != nil {
		return err
	}
	for _, role := range readerRoles {
		err = validateJournalIdentifier(role)
		if err != nil {
			return err
		}
	}
	return adapter.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		var owned bool
		err := tx.NewRaw(`
			SELECT count(*) = 3 FROM pg_catalog.pg_class c
			JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			WHERE n.nspname = ? AND n.nspowner = current_user::regrole::oid
				AND c.relowner = n.nspowner AND c.relkind = 'r'
				AND c.relname IN ('state', 'batches', 'changes')
		`, schemaName).Scan(ctx, &owned)
		if err != nil {
			return err
		}
		if !owned {
			return fmt.Errorf("cleanup installation requires the journal owner and existing journal tables")
		}
		var source journalSource
		err = tx.NewRaw(`
			SELECT c.relowner AS owner_oid, n.nspowner AS schema_owner_oid
			FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
			JOIN pg_catalog.pg_trigger t ON t.tgrelid = c.oid AND t.tgname = 'casbin_journal_change'
			JOIN pg_catalog.pg_proc f ON f.oid = t.tgfoid
			JOIN pg_catalog.pg_namespace fn ON fn.oid = f.pronamespace
			WHERE n.nspname = ? AND c.relname = ? AND fn.nspname = ? AND f.proname = 'record_change'
		`, adapter.matcher.SchemaName, adapter.matcher.TableName, schemaName).Scan(ctx, &source)
		if err != nil {
			return err
		}
		err = adapter.createJournalCleanup(ctx, tx, schemaName, readerRoles)
		if err != nil {
			return err
		}
		return grantJournalReaders(ctx, tx, schemaName, source, readerRoles)
	})
}

func (adapter *BunAdapter) createJournalCleanup(ctx context.Context, tx bun.Tx, schemaName string, readerRoles []string) error {
	schema := adapter.Formatter().FormatQuery("?", bun.Name(schemaName))
	_, err := tx.ExecContext(ctx, `CREATE OR REPLACE FUNCTION ?.prune_changes() RETURNS integer
		LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog AS ?`, bun.Name(schemaName), fmt.Sprintf(journalCleanupSQL, schema, journalCleanupBatchLimit))
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "REVOKE ALL ON FUNCTION ?.prune_changes() FROM PUBLIC", bun.Name(schemaName))
	if err != nil {
		return err
	}
	var grants []string
	err = tx.NewRaw(`
		SELECT r.rolname FROM pg_catalog.pg_proc p
		JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
		CROSS JOIN LATERAL pg_catalog.aclexplode(p.proacl) acl
		JOIN pg_catalog.pg_roles r ON r.oid = acl.grantee
		WHERE n.nspname = ? AND p.proname = 'prune_changes' AND p.pronargs = 0
			AND acl.grantee <> p.proowner
	`, schemaName).Scan(ctx, &grants)
	if err != nil {
		return err
	}
	for _, grantee := range grants {
		allowed := false
		for _, reader := range readerRoles {
			if reader == grantee {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("unexpected cleanup function grant to role %q", grantee)
		}
	}
	return nil
}

// PruneJournal removes up to 1000 complete transactions older than seven days.
func (adapter *BunAdapter) PruneJournal(ctx context.Context, schemaName string) (int, error) {
	err := validateJournalIdentifier(schemaName)
	if err != nil {
		return 0, err
	}
	var removed int
	err = adapter.NewRaw("SELECT ?.prune_changes()", bun.Name(schemaName)).Scan(ctx, &removed)
	return removed, err
}

const journalCleanupSQL = `
DECLARE
	expired bigint[];
	removed integer;
	cutoff timestamptz := pg_catalog.clock_timestamp() - interval '7 days';
BEGIN
	PERFORM revision FROM %[1]s.state WHERE singleton FOR UPDATE SKIP LOCKED;
	IF NOT FOUND THEN
		RETURN 0;
	END IF;
	SELECT pg_catalog.array_agg(revision) INTO expired
	FROM (
		SELECT revision, pg_catalog.max(created_at) OVER (ORDER BY revision) AS newest
		FROM %[1]s.batches ORDER BY revision LIMIT %[2]d
	) oldest WHERE newest < cutoff;
	IF expired IS NULL THEN
		RETURN 0;
	END IF;
	DELETE FROM %[1]s.changes WHERE revision = ANY(expired);
	DELETE FROM %[1]s.batches WHERE revision = ANY(expired);
	GET DIAGNOSTICS removed = ROW_COUNT;
	RETURN removed;
END;
`
