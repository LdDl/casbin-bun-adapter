package casbinbunadapter

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/casbin/casbin/v2"
	"github.com/pkg/errors"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/driver/pgdriver"
)

var (
	triggerTemplate = `
  create%[1]s trigger %[2]s_%[3]s_%[4]s
  after
  insert or update or delete
  on
  %[2]s.%[3]s for each row execute function
  %[5]s.%[6]s();
  `
	triggerProcedureTemplate = `
  CREATE%[1]s FUNCTION %[2]s.%[3]s()
  RETURNS trigger
  LANGUAGE plpgsql
  AS $function$
    begin
      if TG_OP = 'INSERT' then
        perform pg_notify(
          '%[4]s',
					jsonb_build_object(
						'event_type', '%[13]s',
						'new', jsonb_build_object(
							'id', new.%[5]s,
							'ptype', new.%[6]s,
							'v0', new.%[7]s,
							'v1', new.%[8]s,
							'v2', new.%[9]s,
							'v3', new.%[10]s,
							'v4', new.%[11]s,
							'v5', new.%[12]s
						)
					)::text
        );
      end if;
      if TG_OP = 'UPDATE' then
        perform pg_notify(
          '%[4]s',
					jsonb_build_object(
						'event_type', '%[14]s',
						'new', jsonb_build_object(
							'id', new.%[5]s,
							'ptype', new.%[6]s,
							'v0', new.%[7]s,
							'v1', new.%[8]s,
							'v2', new.%[9]s,
							'v3', new.%[10]s,
							'v4', new.%[11]s,
							'v5', new.%[12]s
						),
						'old', jsonb_build_object(
							'id', old.%[5]s,
							'ptype', old.%[6]s,
							'v0', old.%[7]s,
							'v1', old.%[8]s,
							'v2', old.%[9]s,
							'v3', old.%[10]s,
							'v4', old.%[11]s,
							'v5', old.%[12]s
						)
					)::text
        );
      end if;
      if TG_OP = 'DELETE' then
        perform pg_notify(
          '%[4]s',
					jsonb_build_object(
						'event_type', '%[15]s',
						'old', jsonb_build_object(
							'id', old.%[5]s,
							'ptype', old.%[6]s,
							'v0', old.%[7]s,
							'v1', old.%[8]s,
							'v2', old.%[9]s,
							'v3', old.%[10]s,
							'v4', old.%[11]s,
							'v5', old.%[12]s
						)
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
func (adapter *BunAdapter) PrepareTrigger() error {
	replaceTr := ""
	if adapter.trigger.TriggerReplace {
		replaceTr = " OR REPLACE"
	}
	triggerBody := fmt.Sprintf(triggerTemplate, replaceTr, adapter.matcher.SchemaName, adapter.matcher.TableName, adapter.trigger.Name, adapter.trigger.FunctionSchemaName, adapter.trigger.FunctionName)
	replaceFn := ""
	if adapter.trigger.FunctionReplace {
		replaceFn = " OR REPLACE"
	}
	triggerProcedureBody := fmt.Sprintf(triggerProcedureTemplate, replaceFn, adapter.trigger.FunctionSchemaName, adapter.trigger.FunctionName, adapter.trigger.ChannelName,
		adapter.matcher.ID,
		adapter.matcher.PType,
		adapter.matcher.V0,
		adapter.matcher.V1,
		adapter.matcher.V2,
		adapter.matcher.V3,
		adapter.matcher.V4,
		adapter.matcher.V5,
		EVENT_PAYLOAD_INSERT,
		EVENT_PAYLOAD_UPDATE,
		EVENT_PAYLOAD_DELETE,
	)
	ctx := context.Background()
	// We should run it in transaction since potential INSERT operation problem
	err := adapter.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
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

func (adapter *BunAdapter) StartUpdatesListening(enforcer *casbin.SyncedEnforcer) error {
	dbChanMessages, err := adapter.initDBListener()
	if err != nil {
		return errors.Wrap(err, "Can't initialize database LISTEN")
	}
	for msg := range dbChanMessages {
		payloadStr := msg.Payload
		payloadData := TriggerDataPayload{}
		err = json.Unmarshal([]byte(payloadStr), &payloadData)
		if err != nil {
			return errors.Wrapf(err, "Can't read payload from database. Payload is: '%s'", payloadStr)
		}
		err = applyPolicyChange(enforcer, payloadData)
		if err != nil {
			return errors.Wrap(err, "Can't apply policy change")
		}
	}

	return nil
}

func (adapter *BunAdapter) initDBListener() (<-chan pgdriver.Notification, error) {
	ctx := context.Background()
	ln := pgdriver.NewListener(adapter.DB)
	err := ln.Listen(ctx, adapter.trigger.ChannelName)
	if err != nil {
		return nil, err
	}
	return ln.Channel(), nil
}

type TriggerEventPayloadType string

var (
	EVENT_PAYLOAD_INSERT = TriggerEventPayloadType("EVENT_CASBIN_INSERT")
	EVENT_PAYLOAD_UPDATE = TriggerEventPayloadType("EVENT_CASBIN_UPDATE")
	EVENT_PAYLOAD_DELETE = TriggerEventPayloadType("EVENT_CASBIN_DELETE")
)

type TriggerDataPayload struct {
	EventType TriggerEventPayloadType `json:"event_type"`
	Old       CasbinPolicy            `json:"old"`
	New       CasbinPolicy            `json:"new"`
}
