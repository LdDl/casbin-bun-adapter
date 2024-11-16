package casbinbunadapter

import (
	"context"
	"errors"
	"fmt"

	"github.com/casbin/casbin/v2/model"
	"github.com/uptrace/bun"
)

// BunAdapter is just wrapper around *bun.DB
type BunAdapter struct {
	*bun.DB
	matcher MatcherOptions
}

var defaultMatcherOpts = MatcherOptions{
	SchemaName: "public",
	TableName:  "casbin_policy",
	ID:         "id",
	PType:      "ptype",
	V0:         "v0",
	V1:         "v1",
	V2:         "v2",
	V3:         "v3",
	V4:         "v4",
	V5:         "v5",
}

// NewBunAdapter returns new *BunAdapter. Connections to database must be provided. Other arguments are optional
func NewBunAdapter(bunConnection *bun.DB, opts ...func(*BunAdapter)) *BunAdapter {
	defaultMatcher := defaultMatcherOpts
	a := &BunAdapter{bunConnection, defaultMatcher}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// WithMatcherOptions overrides default matching options. If some of keys are empty strings than default values will be applied
func WithMatcherOptions(matcher MatcherOptions) func(*BunAdapter) {
	return func(a *BunAdapter) {
		a.matcher = matcher
		if a.matcher.SchemaName == "" {
			a.matcher.SchemaName = defaultMatcherOpts.SchemaName
		}
		if a.matcher.TableName == "" {
			a.matcher.TableName = defaultMatcherOpts.TableName
		}
		if a.matcher.ID == "" {
			a.matcher.ID = defaultMatcherOpts.ID
		}
		if a.matcher.PType == "" {
			a.matcher.PType = defaultMatcherOpts.PType
		}
		if a.matcher.V0 == "" {
			a.matcher.V0 = defaultMatcherOpts.V0
		}
		if a.matcher.V1 == "" {
			a.matcher.V1 = defaultMatcherOpts.V1
		}
		if a.matcher.V2 == "" {
			a.matcher.V2 = defaultMatcherOpts.V2
		}
		if a.matcher.V3 == "" {
			a.matcher.V3 = defaultMatcherOpts.V3
		}
		if a.matcher.V4 == "" {
			a.matcher.V4 = defaultMatcherOpts.V4
		}
		if a.matcher.V5 == "" {
			a.matcher.V5 = defaultMatcherOpts.V5
		}
	}
}

// LoadPolicy loads all policy rules from the storage
func (a *BunAdapter) LoadPolicy(model model.Model) error {
	var data []CasbinPolicy
	ctx := context.Background()
	query := a.DB.NewSelect().
		Model(&data).
		ModelTableExpr("?.? as t", bun.Name(a.matcher.SchemaName), bun.Name(a.matcher.TableName)).
		ColumnExpr("? as id", bun.Name(a.matcher.ID)).
		ColumnExpr("? as ptype", bun.Name(a.matcher.PType)).
		ColumnExpr("? as v0", bun.Name(a.matcher.V0)).
		ColumnExpr("? as v1", bun.Name(a.matcher.V1)).
		ColumnExpr("? as v2", bun.Name(a.matcher.V2)).
		ColumnExpr("? as v3", bun.Name(a.matcher.V3)).
		ColumnExpr("? as v4", bun.Name(a.matcher.V4)).
		ColumnExpr("? as v5", bun.Name(a.matcher.V5))
	fmt.Println(query)
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
	panic("Not implemented")
	return nil
}

// SavePolicy saves all policy rules to the storage
func (a *BunAdapter) SavePolicy(model model.Model) error {
	return errors.New("not implemented")
}

// AddPolicy adds a policy rule to the storage. Needed for AutoSave, see the ref. https://casbin.org/docs/adapters/#autosave
func (a *BunAdapter) AddPolicy(sec string, ptype string, rule []string) error {
	return errors.New("not implemented")
}

// RemovePolicy removes a policy rule from the storage. Needed for AutoSave, see the ref. https://casbin.org/docs/adapters/#autosave
func (a *BunAdapter) RemovePolicy(sec string, ptype string, rule []string) error {
	return errors.New("not implemented")
}

// RemoveFilteredPolicy removes policy rules that match the filter from the storage. Needed for AutoSave, see the ref. https://casbin.org/docs/adapters/#autosave
func (a *BunAdapter) RemoveFilteredPolicy(sec string, ptype string, fieldIndex int, fieldValues ...string) error {
	return errors.New("not implemented")
}
