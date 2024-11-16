package casbinbunadapter

import (
	"context"
	"fmt"

	"github.com/uptrace/bun"
)

var (
	triggerTemplate = `
  create trigger %[1]s_%[2]s_%[3]s
  after
  insert
  on
  %[1]s.%[2]s for each row execute function
  %[4]s.%[5]s();
  `
	triggerProcedureTemplate = `
  CREATE%[1]s FUNCTION %[2]s.%[3]s()
  RETURNS trigger
  LANGUAGE plpgsql
  AS $function$
    begin
      if TG_OP = 'UPDATE' then
        perform pg_notify(
          '%[4]s',
          jsonb_build_object(
            'id', old.%[5]s,
            'pt_type', new.%[5]s,
            'v0', new.%[6]s,
            'v1', new.%[7]s,
            'v2', new.%[8]s,
            'v3', new.%[9]s,
            'v4', new.%[10]s,
            'v5', new.%[11]s
          )::text
        );
      end if;
      RETURN NEW;
    end;
  $function$
  ;
	`
)

// BuildTrigger creates function and trigger for sending database data changes payload.
// It will check if function exists and if not creates it
// It will check if trigger exists and if not creates it.
// Finalized function name will match following template: "$SCHEMA_NAME$.$FUNCTION_NAME$"
// Finalized trigger name will match following template: "$SCHEMA_NAME$_$TABLE_NAME$_$TRIGGER_NAME$"
func (a *BunAdapter) PrepareTrigger() error {
	triggerBody := fmt.Sprintf(triggerTemplate, a.matcher.SchemaName, a.matcher.TableName, a.trigger.Name, a.trigger.FunctionSchemaName, a.trigger.FunctionName)
	replaceFn := ""
	if a.trigger.FunctionReplace {
		replaceFn = " OR REPLACE"
	}
	triggerProcedureBody := fmt.Sprintf(triggerProcedureTemplate, replaceFn, a.trigger.FunctionSchemaName, a.trigger.FunctionName, a.trigger.ChannelName,
		a.matcher.ID,
		a.matcher.PType,
		a.matcher.V0,
		a.matcher.V1,
		a.matcher.V2,
		a.matcher.V3,
		a.matcher.V4,
		a.matcher.V5,
	)
	ctx := context.Background()
	// We should run it in transaction since potential INSERT operation problem
	err := a.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		triggerProcedureQuery := fmt.Sprintf(
			`
				DO $$
				begin
				  %s	
				EXCEPTION WHEN duplicate_function THEN RAISE NOTICE '%% already exists. Skipping trigger creation',
				SQLERRM USING ERRCODE = SQLSTATE;
				END $$;
			`, triggerProcedureBody)
		_, err := tx.ExecContext(ctx, triggerProcedureQuery)
		if err != nil {
			return err
		}
		triggerQuery := fmt.Sprintf(
			`
				DO $$
				begin
				  %s
				EXCEPTION WHEN duplicate_object THEN RAISE NOTICE '%% already exists. Skipping trigger creation',
				SQLERRM USING ERRCODE = SQLSTATE;
				END $$;
			`, triggerBody)
		_, err = tx.ExecContext(ctx, triggerQuery)
		return err
	})
	return err
}
