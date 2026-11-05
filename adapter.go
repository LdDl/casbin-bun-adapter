package casbinbunadapter

import (
	"context"
	"fmt"

	"github.com/casbin/casbin/v2/model"
	"github.com/pkg/errors"
	"github.com/uptrace/bun"
)

// BunAdapter is just wrapper around *bun.DB
type BunAdapter struct {
	*bun.DB
	matcher MatcherOptions
	trigger TriggerOptions
}

// NewBunAdapter returns new *BunAdapter. Connections to database must be provided. Other arguments are optional
func NewBunAdapter(bunConnection *bun.DB, opts ...func(*BunAdapter)) *BunAdapter {
	defaultMatcher := defaultMatcherOpts
	defaultTrigger := defaultTriggerOpts
	a := &BunAdapter{bunConnection, defaultMatcher, defaultTrigger}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// LoadPolicy loads all policy rules from the storage
func (adapter *BunAdapter) LoadPolicy(model model.Model) error {
	var data []CasbinPolicy
	ctx := context.Background()
	query := adapter.NewSelect().
		Model(&data).
		ModelTableExpr("?.? as t", bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName)).
		ColumnExpr("? as id", bun.Name(adapter.matcher.ID)).
		ColumnExpr("? as ptype", bun.Name(adapter.matcher.PType)).
		ColumnExpr("? as v0", bun.Name(adapter.matcher.V0)).
		ColumnExpr("? as v1", bun.Name(adapter.matcher.V1)).
		ColumnExpr("? as v2", bun.Name(adapter.matcher.V2)).
		ColumnExpr("? as v3", bun.Name(adapter.matcher.V3)).
		ColumnExpr("? as v4", bun.Name(adapter.matcher.V4)).
		ColumnExpr("? as v5", bun.Name(adapter.matcher.V5))
	err := query.Scan(ctx)
	if err != nil {
		return err
	}
	for i := range data {
		row := data[i]
		err = loadSinglePolicy(row, model)
		if err != nil {
			return err
		}
	}
	return nil
}

func loadSinglePolicy(policy CasbinPolicy, model model.Model) error {
	err := validatePolicyType("", policy.PType)
	if err != nil {
		return err
	}
	ruleDef, err := policy.getRuleDefinition()
	if err != nil {
		return err
	}
	found, err := model.HasPolicyEx(policy.PType[:1], policy.PType, ruleDef)
	if err != nil {
		return errors.Wrapf(err, "Can't validate single policy. Policy: '%+v'", policy)
	}
	if found {
		// Just skip existing policy
		return nil
	}
	err = model.AddPolicy(policy.PType[:1], policy.PType, ruleDef)
	if err != nil {
		return errors.Wrapf(err, "Can't load single policy. Policy: '%+v'", policy)
	}
	return nil
}

// SavePolicy saves all policy rules to the storage
func (adapter *BunAdapter) SavePolicy(model model.Model) error {
	policies := []CasbinPolicy{}

	/* Collect policies and rules */
	if p, ok := model["p"]; ok {
		for ptype, ast := range p {
			err := validatePolicyType("p", ptype)
			if err != nil {
				return err
			}
			for _, ruleDef := range ast.Policy {
				err := validatePolicy(ptype[:1], ptype, ruleDef)
				if err != nil {
					return err
				}
				_, err = model.HasPolicyEx(ptype[:1], ptype, ruleDef)
				if err != nil {
					return err
				}
				policy, err := NewCasbinPolicyFrom(ptype, ruleDef)
				if err != nil {
					return err
				}
				policies = append(policies, policy)
			}
		}
	}

	if g, ok := model["g"]; ok {
		for ptype, ast := range g {
			err := validatePolicyType("g", ptype)
			if err != nil {
				return err
			}
			for _, ruleDef := range ast.Policy {
				err := validatePolicy(ptype[:1], ptype, ruleDef)
				if err != nil {
					return err
				}
				_, err = model.HasPolicyEx(ptype[:1], ptype, ruleDef)
				if err != nil {
					return err
				}
				policy, err := NewCasbinPolicyFrom(ptype, ruleDef)
				if err != nil {
					return err
				}
				policies = append(policies, policy)
			}
		}
	}

	/* Update table data */
	err := adapter.savePoliciesToDB(policies)
	if err != nil {
		return errors.Wrap(err, "Can't save policies to the database")
	}
	return nil
}

func (adapter *BunAdapter) savePoliciesToDB(policies []CasbinPolicy) error {
	ctx := context.Background()
	// We should run it in transaction since potential INSERT operation problem
	err := adapter.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		/* Clean table first */
		truncateQuery := tx.NewTruncateTable().
			ModelTableExpr("?.?", bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName)).
			Model((*CasbinPolicy)(nil))
		_, err := truncateQuery.Exec(ctx)
		if err != nil {
			return err
		}
		/* Insert policies */
		// Since it is hard to change column name, just insert it a loop instead of bulk insert
		for i := range policies {
			policy := policies[i]
			// https://bun.uptrace.dev/guide/query-insert.html#maps
			values := map[string]interface{}{
				adapter.matcher.PType: policy.PType,
				adapter.matcher.V0:    policy.V0,
				adapter.matcher.V1:    policy.V1,
				adapter.matcher.V2:    policy.V2,
				adapter.matcher.V3:    policy.V3,
				adapter.matcher.V4:    policy.V4,
				adapter.matcher.V5:    policy.V5,
			}
			query := tx.NewInsert().
				ModelTableExpr("?.?", bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName)).
				Model(&values)
			_, err = query.Exec(ctx)
			if err != nil {
				return errors.Wrapf(err, "Can't insert single policy. Policy: %+v", policy)
			}
		}
		return nil
	})
	return err
}

// AddPolicy adds a policy rule to the storage. Needed for AutoSave, see the ref. https://casbin.org/docs/adapters/#autosave
func (adapter *BunAdapter) AddPolicy(sec string, ptype string, rule []string) error {
	err := validatePolicy(sec, ptype, rule)
	if err != nil {
		return err
	}
	ctx := context.Background()
	newPolicy, err := NewCasbinPolicyFrom(ptype, rule)
	if err != nil {
		return err
	}
	values := map[string]interface{}{
		adapter.matcher.PType: newPolicy.PType,
		adapter.matcher.V0:    newPolicy.V0,
		adapter.matcher.V1:    newPolicy.V1,
		adapter.matcher.V2:    newPolicy.V2,
		adapter.matcher.V3:    newPolicy.V3,
		adapter.matcher.V4:    newPolicy.V4,
		adapter.matcher.V5:    newPolicy.V5,
	}
	query := adapter.DB.NewInsert().
		ModelTableExpr("?.?", bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName)).
		On("CONFLICT (?, ?, ?, ?, ?, ?, ?) DO NOTHING", bun.Name(adapter.matcher.PType), bun.Name(adapter.matcher.V0), bun.Name(adapter.matcher.V1), bun.Name(adapter.matcher.V2), bun.Name(adapter.matcher.V3), bun.Name(adapter.matcher.V4), bun.Name(adapter.matcher.V5)).
		Model(&values)
	_, err = query.Exec(ctx)
	return err
}

// RemovePolicy removes a policy rule from the storage. Needed for AutoSave, see the ref. https://casbin.org/docs/adapters/#autosave
func (adapter *BunAdapter) RemovePolicy(sec string, ptype string, rule []string) error {
	err := validatePolicy(sec, ptype, rule)
	if err != nil {
		return err
	}
	ctx := context.Background()
	query := adapter.DB.NewDelete().
		ModelTableExpr("?.?", bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName)).
		Where("? = ?", bun.Name(adapter.matcher.PType), ptype)
	columns := []string{adapter.matcher.V0, adapter.matcher.V1, adapter.matcher.V2, adapter.matcher.V3, adapter.matcher.V4, adapter.matcher.V5}
	for i, column := range columns {
		if i < len(rule) {
			query = query.Where("? = ?", bun.Name(column), rule[i])
		} else {
			query = query.Where("? IS NULL", bun.Name(column))
		}
	}
	_, err = query.Exec(ctx)
	return err
}

// RemoveFilteredPolicy removes policy rules that match the filter from the storage. Needed for AutoSave, see the ref. https://casbin.org/docs/adapters/#autosave
func (adapter *BunAdapter) RemoveFilteredPolicy(sec string, ptype string, fieldIndex int, fieldValues ...string) error {
	if sec != "p" && sec != "g" {
		return fmt.Errorf("policy section must be p or g")
	}
	err := validatePolicyType(sec, ptype)
	if err != nil {
		return err
	}
	if fieldIndex < 0 || fieldIndex >= 6 || len(fieldValues) > 6-fieldIndex {
		return fmt.Errorf("policy filter exceeds fields v0 through v5")
	}
	err = validatePolicyValues(fieldValues)
	if err != nil {
		return err
	}
	ctx := context.Background()
	query := adapter.DB.NewDelete().
		ModelTableExpr("?.?", bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName)).
		Where("? = ?", bun.Name(adapter.matcher.PType), ptype)
	if v := extractRuleField(0, fieldIndex, fieldValues...); v != "" {
		query = query.Where("? = ?", bun.Name(adapter.matcher.V0), v)
	}
	if v := extractRuleField(1, fieldIndex, fieldValues...); v != "" {
		query = query.Where("? = ?", bun.Name(adapter.matcher.V1), v)
	}
	if v := extractRuleField(2, fieldIndex, fieldValues...); v != "" {
		query = query.Where("? = ?", bun.Name(adapter.matcher.V2), v)
	}
	if v := extractRuleField(3, fieldIndex, fieldValues...); v != "" {
		query = query.Where("? = ?", bun.Name(adapter.matcher.V3), v)
	}
	if v := extractRuleField(4, fieldIndex, fieldValues...); v != "" {
		query = query.Where("? = ?", bun.Name(adapter.matcher.V4), v)
	}
	if v := extractRuleField(5, fieldIndex, fieldValues...); v != "" {
		query = query.Where("? = ?", bun.Name(adapter.matcher.V5), v)
	}
	_, err = query.Exec(ctx)
	return err
}

func extractRuleField(fieldArrayIdx, fieldIndex int, fieldValues ...string) string {
	var val string
	if fieldIndex <= fieldArrayIdx && fieldArrayIdx < fieldIndex+len(fieldValues) {
		val = fieldValues[fieldArrayIdx-fieldIndex]
	}
	return val
}
