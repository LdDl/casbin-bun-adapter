package casbinbunadapter

import (
	"fmt"

	"github.com/casbin/casbin/v2"
)

func applyPolicyChange(enforcer *casbin.SyncedEnforcer, change TriggerDataPayload) error {
	var oldRule, newRule []string
	var err error
	switch change.EventType {
	case EVENT_PAYLOAD_INSERT:
		newRule, err = change.New.getRuleDefinition()
	case EVENT_PAYLOAD_DELETE:
		oldRule, err = change.Old.getRuleDefinition()
	case EVENT_PAYLOAD_UPDATE:
		oldRule, err = change.Old.getRuleDefinition()
		if err != nil {
			return err
		}
		newRule, err = change.New.getRuleDefinition()
	default:
		return fmt.Errorf("unknown policy change type %q", change.EventType)
	}
	if err != nil {
		return err
	}

	// Validate both sides before removing a rule during an update.
	lock := enforcer.GetLock()
	lock.RLock()
	if oldRule != nil {
		_, err = enforcer.GetModel().HasPolicyEx(change.Old.PType[:1], change.Old.PType, oldRule)
	}
	if err == nil && newRule != nil {
		_, err = enforcer.GetModel().HasPolicyEx(change.New.PType[:1], change.New.PType, newRule)
	}
	lock.RUnlock()
	if err != nil {
		return err
	}

	if oldRule != nil {
		if change.Old.PType[0] == 'p' {
			_, err = enforcer.RemoveNamedPolicy(change.Old.PType, oldRule)
		} else {
			_, err = enforcer.RemoveNamedGroupingPolicy(change.Old.PType, oldRule)
		}
		if err != nil {
			return err
		}
	}
	if newRule != nil {
		if change.New.PType[0] == 'p' {
			_, err = enforcer.AddNamedPolicy(change.New.PType, newRule)
		} else {
			_, err = enforcer.AddNamedGroupingPolicy(change.New.PType, newRule)
		}
	}
	return err
}
