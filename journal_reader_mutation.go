package casbinbunadapter

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

type journalMutation struct {
	PType  string
	Rule   []string
	Remove bool
}

type journalRuleKey struct {
	PType  string
	Values string
}

func prepareJournalMutations(ctx context.Context, m model.Model, changes []journalChange) ([]journalMutation, error) {
	var mutations []journalMutation
	presence := make(map[journalRuleKey]bool)
	for _, change := range changes {
		err := ctx.Err()
		if err != nil {
			return nil, err
		}
		if change.Operation != "INSERT" && change.Operation != "UPDATE" && change.Operation != "DELETE" {
			return nil, fmt.Errorf("unknown journal operation %q", change.Operation)
		}
		if change.Operation != "INSERT" {
			mutation, err := prepareJournalMutation(m, presence, change.OldPolicy, true)
			if err != nil {
				return nil, fmt.Errorf("journal batch %d: %w", change.Revision, err)
			}
			mutations = append(mutations, mutation)
		}
		if change.Operation != "DELETE" {
			mutation, err := prepareJournalMutation(m, presence, change.NewPolicy, false)
			if err != nil {
				return nil, fmt.Errorf("journal batch %d: %w", change.Revision, err)
			}
			mutations = append(mutations, mutation)
		}
	}
	return mutations, nil
}

func prepareJournalMutation(m model.Model, presence map[journalRuleKey]bool, data json.RawMessage, remove bool) (journalMutation, error) {
	var mutation journalMutation
	var policy CasbinPolicy
	err := json.Unmarshal(data, &policy)
	if err != nil {
		return mutation, err
	}
	err = validatePolicyType("", policy.PType)
	if err != nil {
		return mutation, err
	}
	rule, err := policy.getRuleDefinition()
	if err != nil {
		return mutation, err
	}
	found, err := m.HasPolicyEx(policy.PType[:1], policy.PType, rule)
	if err != nil {
		return mutation, err
	}
	key := journalRuleKey{PType: policy.PType, Values: strings.Join(rule, model.DefaultSep)}
	if pending, exists := presence[key]; exists {
		found = pending
	}
	if remove && !found {
		return mutation, fmt.Errorf("journal removes a policy absent from the local model")
	}
	if !remove && found {
		return mutation, fmt.Errorf("duplicate policy in journal or policy table")
	}
	presence[key] = !remove
	return journalMutation{PType: policy.PType, Rule: rule, Remove: remove}, nil
}

func applyJournalModelMutations(m model.Model, mutations []journalMutation) error {
	for _, mutation := range mutations {
		section := mutation.PType[:1]
		if mutation.Remove {
			changed, err := m.RemovePolicy(section, mutation.PType, mutation.Rule)
			if err != nil {
				return err
			}
			if !changed {
				return fmt.Errorf("canonical policy model does not match the journal")
			}
		} else {
			err := m.AddPolicy(section, mutation.PType, mutation.Rule)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func applyJournalEnforcerMutations(ctx context.Context, enforcer *casbin.Enforcer, mutations []journalMutation) error {
	for _, mutation := range mutations {
		err := ctx.Err()
		if err != nil {
			return err
		}
		var changed bool
		if mutation.PType[:1] == "g" {
			if mutation.Remove {
				changed, err = enforcer.RemoveNamedGroupingPolicy(mutation.PType, mutation.Rule)
			} else {
				changed, err = enforcer.AddNamedGroupingPolicy(mutation.PType, mutation.Rule)
			}
		} else if mutation.Remove {
			changed, err = enforcer.RemoveNamedPolicy(mutation.PType, mutation.Rule)
		} else {
			changed, err = enforcer.AddNamedPolicy(mutation.PType, mutation.Rule)
		}
		if err != nil {
			return err
		}
		if !changed {
			return fmt.Errorf("private enforcer does not match the journal")
		}
		if mutation.Remove && mutation.PType[:1] == "g" {
			err = restoreJournalRoleLink(enforcer, mutation)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func restoreJournalRoleLink(enforcer *casbin.Enforcer, mutation journalMutation) error {
	assertion := enforcer.GetModel()["g"][mutation.PType]
	count := strings.Count(assertion.Value, "_")
	// Grouping rules with different trailing fields may represent the same role link.
	for _, rule := range assertion.Policy {
		if slices.Equal(rule[:count], mutation.Rule[:count]) {
			return enforcer.BuildIncrementalRoleLinks(model.PolicyAdd, mutation.PType, [][]string{rule})
		}
	}
	return nil
}
