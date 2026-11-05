package casbinbunadapter

import (
	"context"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/constant"
	"github.com/casbin/casbin/v2/model"
)

// Checks pin the current view under viewMu; only an unpinned spare may be mutated.
type journalView struct {
	enforcer    *casbin.Enforcer
	users       atomic.Int64
	incremental bool
	replay      []journalMutation
}

type journalPublication struct {
	View      *journalView
	Spare     *journalView
	Model     model.Model
	Mutations []journalMutation
	// Replay advances the spare to the published revision before its next use.
	Replay []journalMutation
}

// preparePublication requires syncGate and never changes the published enforcer.
func (reader *JournalReader) preparePublication(ctx context.Context, update journalUpdate) (*journalPublication, error) {
	reset := update.Model != nil
	for _, change := range update.Changes {
		if change.Operation == "TRUNCATE" {
			reset = true
		}
	}
	var mutations []journalMutation
	var err error
	if !reset {
		mutations, err = prepareJournalMutations(ctx, reader.model, update.Changes)
		if err != nil {
			return nil, err
		}
	}
	incremental := !reset && reader.view != nil && reader.view.incremental && journalMutationPrioritiesValid(reader.template, mutations)
	if incremental && reader.spare != nil && reader.spare.users.Load() == 0 {
		candidate := reader.spare
		// An interrupted update discards the private copy, including partially applied changes.
		reader.spare = nil
		err = applyJournalEnforcerMutations(ctx, candidate.enforcer, candidate.replay)
		if err != nil {
			return nil, err
		}
		candidate.replay = nil
		err = applyJournalEnforcerMutations(ctx, candidate.enforcer, mutations)
		if err != nil {
			return nil, err
		}
		return &journalPublication{
			View: candidate, Spare: reader.view, Model: reader.model,
			Mutations: mutations, Replay: mutations,
		}, nil
	}

	next := update.Model
	if next == nil {
		next = copyJournalModel(reader.model)
		for _, change := range update.Changes {
			err = ctx.Err()
			if err != nil {
				return nil, err
			}
			err = applyJournalChange(next, change)
			if err != nil {
				return nil, fmt.Errorf("journal batch %d: %w", change.Revision, err)
			}
		}
	}
	candidate, err := reader.buildJournalView(next)
	if err != nil {
		return nil, err
	}
	publication := &journalPublication{View: candidate, Model: next}
	if candidate.incremental {
		if incremental {
			publication.Spare = reader.view
			publication.Replay = mutations
		} else {
			err = ctx.Err()
			if err != nil {
				return nil, err
			}
			publication.Spare, err = reader.buildJournalView(next)
			if err != nil {
				return nil, err
			}
		}
	}
	return publication, nil
}

func (reader *JournalReader) buildJournalView(m model.Model) (*journalView, error) {
	enforcer, err := reader.buildEnforcer(m)
	if err != nil {
		return nil, err
	}
	view := &journalView{enforcer: enforcer}
	if reader.options.Configure != nil {
		return view, nil
	}
	for _, assertion := range m["g"] {
		if len(assertion.ParamsTokens) != 0 {
			return view, nil
		}
	}
	for _, assertion := range m["e"] {
		if assertion.Value == constant.SubjectPriorityEffect {
			return view, nil
		}
	}
	for ptype, assertion := range m["p"] {
		for _, rule := range assertion.Policy {
			if !journalPriorityValid(m, ptype, rule) {
				return view, nil
			}
		}
	}
	view.incremental = true
	return view, nil
}

func journalMutationPrioritiesValid(m model.Model, mutations []journalMutation) bool {
	for _, mutation := range mutations {
		if !mutation.Remove && mutation.PType[:1] == "p" && !journalPriorityValid(m, mutation.PType, mutation.Rule) {
			return false
		}
	}
	return true
}

func journalPriorityValid(m model.Model, ptype string, rule []string) bool {
	assertion := m["p"][ptype]
	index, found := assertion.FieldIndexMap[constant.PriorityIndex]
	if !found {
		for position, token := range assertion.Tokens {
			if token == ptype+"_priority" {
				index, found = position, true
				break
			}
		}
	}
	if !found {
		return true
	}
	if index < 0 || index >= len(rule) {
		return false
	}
	_, err := strconv.Atoi(rule[index])
	return err == nil
}
