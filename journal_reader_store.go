package casbinbunadapter

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/casbin/casbin/v2/model"
	"github.com/uptrace/bun"
)

type journalHead struct {
	Revision int64
	Identity int64
}

type journalChange struct {
	Revision  int64
	Operation string
	OldPolicy json.RawMessage
	NewPolicy json.RawMessage
}

type journalUpdate struct {
	Identity int64
	Head     int64
	Revision int64
	Model    model.Model
}

func (reader *JournalReader) readHead(ctx context.Context) (journalHead, error) {
	var head journalHead
	err := reader.adapter.NewRaw("SELECT revision, tableoid::bigint AS identity FROM ?.state WHERE singleton", bun.Name(reader.schema)).Scan(ctx, &head)
	return head, err
}

func (reader *JournalReader) read(ctx context.Context, cursor int64, target *journalHead) (journalUpdate, error) {
	var update journalUpdate
	err := reader.adapter.RunInTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}, func(ctx context.Context, tx bun.Tx) error {
		var head journalHead
		err := tx.NewRaw("SELECT revision, tableoid::bigint AS identity FROM ?.state WHERE singleton", bun.Name(reader.schema)).Scan(ctx, &head)
		if err != nil {
			return err
		}
		update = journalUpdate{Identity: head.Identity, Head: head.Revision, Revision: cursor}
		limit := head.Revision
		if target != nil {
			if head.Identity != target.Identity || head.Revision < target.Revision {
				return fmt.Errorf("policy journal changed during synchronization")
			}
			limit = target.Revision
		}
		reload := reader.identity != head.Identity || cursor > head.Revision
		if !reload && cursor >= limit {
			return nil
		}
		if !reload {
			var revisions []int64
			err = tx.NewSelect().TableExpr("?.batches", bun.Name(reader.schema)).
				Column("revision").Where("revision > ? AND revision <= ?", cursor, limit).
				Order("revision").Limit(reader.options.BatchLimit).Scan(ctx, &revisions)
			if err != nil {
				return err
			}
			// Cleanup only removes a prefix. A missing next revision requires a fresh snapshot.
			reload = len(revisions) == 0 || revisions[0] != cursor+1
			if !reload {
				for i, revision := range revisions {
					if revision != cursor+int64(i)+1 || revision > limit {
						return fmt.Errorf("non-contiguous policy journal")
					}
				}
				var changes []journalChange
				err = tx.NewSelect().TableExpr("?.changes", bun.Name(reader.schema)).
					Column("revision", "operation", "old_policy", "new_policy").
					Where("revision IN (?)", bun.In(revisions)).Order("revision", "id").Scan(ctx, &changes)
				if err != nil {
					return err
				}
				update.Model = copyJournalModel(reader.model)
				position := 0
				for _, revision := range revisions {
					if position == len(changes) || changes[position].Revision != revision {
						return fmt.Errorf("empty journal batch %d", revision)
					}
					for position < len(changes) && changes[position].Revision == revision {
						err = applyJournalChange(update.Model, changes[position])
						if err != nil {
							return fmt.Errorf("journal batch %d: %w", revision, err)
						}
						position++
					}
				}
				update.Revision = revisions[len(revisions)-1]
				return nil
			}
		}
		var attached bool
		err = tx.NewRaw(`
			SELECT EXISTS (
				SELECT FROM pg_catalog.pg_trigger t
				JOIN pg_catalog.pg_proc f ON f.oid = t.tgfoid
				JOIN pg_catalog.pg_namespace fn ON fn.oid = f.pronamespace
				JOIN pg_catalog.pg_class c ON c.oid = t.tgrelid
				JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
				WHERE fn.nspname = ? AND f.proname = 'record_change'
					AND t.tgname = 'casbin_journal_change' AND t.tgenabled = 'A'
					AND n.nspname = ? AND c.relname = ?
			)
		`, reader.schema, reader.adapter.matcher.SchemaName, reader.adapter.matcher.TableName).Scan(ctx, &attached)
		if err != nil {
			return err
		}
		if !attached {
			return fmt.Errorf("journal is not attached to the configured policy table")
		}
		m := reader.adapter.matcher
		var policies []CasbinPolicy
		err = tx.NewSelect().Model(&policies).ModelTableExpr("?.? AS t", bun.Name(m.SchemaName), bun.Name(m.TableName)).
			ColumnExpr("? AS ptype", bun.Name(m.PType)).
			ColumnExpr("? AS v0", bun.Name(m.V0)).ColumnExpr("? AS v1", bun.Name(m.V1)).
			ColumnExpr("? AS v2", bun.Name(m.V2)).ColumnExpr("? AS v3", bun.Name(m.V3)).
			ColumnExpr("? AS v4", bun.Name(m.V4)).ColumnExpr("? AS v5", bun.Name(m.V5)).
			OrderExpr("? ASC", bun.Name(m.ID)).Scan(ctx)
		if err != nil {
			return err
		}
		update.Model = copyJournalModel(reader.template)
		for _, policy := range policies {
			err = changeJournalPolicy(update.Model, policy, false)
			if err != nil {
				return err
			}
		}
		update.Revision = head.Revision
		return nil
	})
	return update, err
}
