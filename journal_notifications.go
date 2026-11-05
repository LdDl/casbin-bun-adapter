package casbinbunadapter

import (
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/uptrace/bun"
)

func journalNotificationChannel(schema string) string {
	digest := sha256.Sum256([]byte(schema))
	return fmt.Sprintf("casbin_journal_%x", digest[:16])
}

// EnableJournalNotifications installs commit notifications for an existing journal as its owner.
func (adapter *BunAdapter) EnableJournalNotifications(ctx context.Context, schemaName string) error {
	err := validateJournalIdentifier(schemaName)
	if err != nil {
		return err
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
			return fmt.Errorf("notification installation requires the journal owner and existing journal tables")
		}
		var attached bool
		err = tx.NewRaw(`
			SELECT EXISTS (
				SELECT FROM pg_catalog.pg_trigger t
				JOIN pg_catalog.pg_proc f ON f.oid = t.tgfoid
				JOIN pg_catalog.pg_namespace fn ON fn.oid = f.pronamespace
				JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
				JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				WHERE n.nspname = ? AND c.relname = ?
					AND fn.nspname = ? AND f.proname = 'record_change'
					AND t.tgname = 'casbin_journal_change' AND t.tgenabled = 'A'
			)
		`, adapter.matcher.SchemaName, adapter.matcher.TableName, schemaName).Scan(ctx, &attached)
		if err != nil {
			return err
		}
		if !attached {
			return fmt.Errorf("journal is not attached to the configured policy table")
		}
		return adapter.createJournalNotifications(ctx, tx, schemaName)
	})
}

func (adapter *BunAdapter) createJournalNotifications(ctx context.Context, tx bun.Tx, schemaName string) error {
	var sourceOID int64
	err := tx.NewRaw(`
		SELECT c.oid FROM pg_catalog.pg_class c
		JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = ? AND c.relname = 'batches'
	`, schemaName).Scan(ctx, &sourceOID)
	if err != nil {
		return err
	}
	channel := adapter.Formatter().FormatQuery("?", journalNotificationChannel(schemaName))
	body := fmt.Sprintf(journalNotificationSQL, sourceOID, channel)
	_, err = tx.ExecContext(ctx, `
		CREATE OR REPLACE FUNCTION ?.notify_change() RETURNS trigger
		LANGUAGE plpgsql SET search_path = pg_catalog AS ?;
		REVOKE ALL ON FUNCTION ?.notify_change() FROM PUBLIC;
		CREATE OR REPLACE TRIGGER casbin_journal_notify
			AFTER INSERT ON ?.batches
			FOR EACH ROW EXECUTE FUNCTION ?.notify_change();
		ALTER TABLE ?.batches ENABLE ALWAYS TRIGGER casbin_journal_notify;
	`, bun.Name(schemaName), body, bun.Name(schemaName), bun.Name(schemaName), bun.Name(schemaName), bun.Name(schemaName))
	if err != nil {
		return err
	}
	var unexpectedGrants bool
	err = tx.NewRaw(`
		SELECT EXISTS (
			SELECT FROM pg_catalog.pg_proc p
			JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace,
				LATERAL pg_catalog.aclexplode(p.proacl) acl
			WHERE n.nspname = ? AND p.proname = 'notify_change'
				AND acl.grantee <> p.proowner
		)
	`, schemaName).Scan(ctx, &unexpectedGrants)
	if err != nil {
		return err
	}
	if unexpectedGrants {
		return fmt.Errorf("notification function has unexpected default privileges or grants to non-owner roles")
	}
	return nil
}

const journalNotificationSQL = `
BEGIN
	IF TG_RELID <> %[1]d::oid OR TG_OP <> 'INSERT' OR TG_LEVEL <> 'ROW' OR TG_WHEN <> 'AFTER' THEN
		RAISE EXCEPTION 'Invalid policy journal notification target';
	END IF;
	PERFORM pg_catalog.pg_notify(%[2]s, '');
	RETURN NULL;
END;
`
