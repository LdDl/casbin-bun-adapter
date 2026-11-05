package casbinbunadapter

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

type journalReaderRole struct {
	Present bool
	Unsafe  bool
}

func grantJournalReaders(ctx context.Context, tx bun.Tx, schemaName string, source journalSource, roles []string) error {
	for _, role := range roles {
		var reader journalReaderRole
		err := tx.NewRaw(`
			SELECT
				EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = ?) AS present,
				EXISTS (
					SELECT FROM pg_catalog.pg_roles reader
					JOIN pg_catalog.pg_roles privileged ON pg_catalog.pg_has_role(reader.oid, privileged.oid, 'MEMBER')
					WHERE reader.rolname = ? AND (
						privileged.rolsuper OR privileged.rolcreaterole OR
						privileged.oid IN (?::oid, ?::oid, current_user::regrole::oid) OR
						privileged.rolname = 'pg_write_all_data'
					)
				) AS unsafe
		`, role, role, source.OwnerOID, source.SchemaOwnerOID).Scan(ctx, &reader)
		if err != nil {
			return err
		}
		if !reader.Present {
			return fmt.Errorf("journal reader role %q does not exist; provide an existing application database role", role)
		}
		if reader.Unsafe {
			return fmt.Errorf("journal reader role %q has owner or administrative privileges; use a separate application database role", role)
		}
		_, err = tx.ExecContext(ctx, `
			GRANT USAGE ON SCHEMA ? TO ?;
			GRANT SELECT ON ?.state, ?.batches, ?.changes TO ?;
		`, bun.Name(schemaName), bun.Name(role), bun.Name(schemaName), bun.Name(schemaName), bun.Name(schemaName), bun.Name(role))
		if err != nil {
			return err
		}
	}
	return nil
}
