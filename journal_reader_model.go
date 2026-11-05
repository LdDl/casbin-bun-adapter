package casbinbunadapter

import (
	"encoding/json"
	"fmt"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

func copyJournalModel(source model.Model) model.Model {
	copy := source.Copy()
	for section, assertions := range source {
		for name, assertion := range assertions {
			copy[section][name].ParamsTokens = append([]string(nil), assertion.ParamsTokens...)
			copy[section][name].FieldIndexMap = make(map[string]int, len(assertion.FieldIndexMap))
			for key, index := range assertion.FieldIndexMap {
				copy[section][name].FieldIndexMap[key] = index
			}
		}
	}
	return copy
}

func (reader *JournalReader) buildEnforcer(m model.Model) (*casbin.Enforcer, error) {
	// Casbin builds role managers and field indexes on its own copy.
	working := copyJournalModel(m)
	err := working.SortPoliciesBySubjectHierarchy()
	if err != nil {
		return nil, err
	}
	err = working.SortPoliciesByPriority()
	if err != nil {
		return nil, err
	}
	e, err := casbin.NewEnforcer(working)
	if err != nil {
		return nil, err
	}
	for ptype, assertion := range working["g"] {
		if len(assertion.ParamsTokens) != 0 {
			err = e.BuildIncrementalConditionalRoleLinks(model.PolicyAdd, ptype, assertion.Policy)
			if err != nil {
				return nil, err
			}
		}
	}
	if reader.options.Configure != nil {
		err = reader.options.Configure(e)
		if err != nil {
			return nil, err
		}
	}
	e.EnableAutoSave(false)
	e.SetAdapter(nil)
	err = e.BuildRoleLinks()
	if err != nil {
		return nil, err
	}
	return e, nil
}

func changeJournalPolicy(m model.Model, policy CasbinPolicy, remove bool) error {
	err := validatePolicyType("", policy.PType)
	if err != nil {
		return err
	}
	rule, err := policy.getRuleDefinition()
	if err != nil {
		return err
	}
	section := policy.PType[:1]
	found, err := m.HasPolicyEx(section, policy.PType, rule)
	if err != nil {
		return err
	}
	if remove {
		if !found {
			return fmt.Errorf("journal removes a policy absent from the local model")
		}
		_, err = m.RemovePolicy(section, policy.PType, rule)
		return err
	}
	if found {
		return fmt.Errorf("duplicate policy in journal or policy table")
	}
	return m.AddPolicy(section, policy.PType, rule)
}

func applyJournalChange(m model.Model, change journalChange) error {
	if change.Operation == "TRUNCATE" {
		m.ClearPolicy()
		return nil
	}
	if change.Operation != "INSERT" && change.Operation != "UPDATE" && change.Operation != "DELETE" {
		return fmt.Errorf("unknown journal operation %q", change.Operation)
	}
	if change.Operation != "INSERT" {
		var old CasbinPolicy
		err := json.Unmarshal(change.OldPolicy, &old)
		if err != nil {
			return err
		}
		err = changeJournalPolicy(m, old, true)
		if err != nil {
			return err
		}
	}
	if change.Operation != "DELETE" {
		var next CasbinPolicy
		err := json.Unmarshal(change.NewPolicy, &next)
		if err != nil {
			return err
		}
		return changeJournalPolicy(m, next, false)
	}
	return nil
}
