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
  insert or delete
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
      if TG_OP = 'DELETE' then
        perform pg_notify(
          '%[4]s',
					jsonb_build_object(
						'event_type', '%[14]s',
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
func (a *BunAdapter) PrepareTrigger() error {
	replaceTr := ""
	if a.trigger.TriggerReplace {
		replaceTr = " OR REPLACE"
	}
	triggerBody := fmt.Sprintf(triggerTemplate, replaceTr, a.matcher.SchemaName, a.matcher.TableName, a.trigger.Name, a.trigger.FunctionSchemaName, a.trigger.FunctionName)
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
		EVENT_PAYLOAD_INSERT,
		EVENT_PAYLOAD_DELETE,
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

func (a *BunAdapter) StartUpdatesListening(enforcer *casbin.SyncedEnforcer) error {
	dbChanMessages, err := a.initDBListener()
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
		switch payloadData.EventType {
		case EVENT_PAYLOAD_INSERT:
			ptype := payloadData.New.PType[:1]
			switch ptype {
			case "p":
				_, err := enforcer.AddPolicy(payloadData.New.getRuleDefinition())
				if err != nil {
					return errors.Wrapf(err, "Bad new policy. Policy is: '%s'", payloadStr)
				}
			case "g":
				_, err := enforcer.AddGroupingPolicy(payloadData.New.getRuleDefinition())
				if err != nil {
					return errors.Wrapf(err, "Bad new grouping policy. Policy is: '%s'", payloadStr)
				}
			}
		case EVENT_PAYLOAD_UPDATE:
			fmt.Println("Need to update")
			// @todo
		case EVENT_PAYLOAD_DELETE:
			ptype := payloadData.Old.PType[:1]
			switch ptype {
			case "p":
				_, err := enforcer.RemovePolicy(payloadData.Old.getRuleDefinition())
				if err != nil {
					return errors.Wrapf(err, "Bad old policy. Policy is: '%s'", payloadStr)
				}
			case "g":
				_, err := enforcer.RemoveGroupingPolicy(payloadData.Old.getRuleDefinition())
				if err != nil {
					return errors.Wrapf(err, "Bad old grouping policy. Policy is: '%s'", payloadStr)
				}
			}
		}
	}

	return nil
}

func (a *BunAdapter) initDBListener() (<-chan pgdriver.Notification, error) {
	ctx := context.Background()
	ln := pgdriver.NewListener(a.DB)
	err := ln.Listen(ctx, a.trigger.ChannelName)
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
