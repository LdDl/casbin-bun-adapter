package casbin_bun_adapter

import (
	"errors"

	"github.com/casbin/casbin/v2/model"
	"github.com/uptrace/bun"
)

// BunAdapter is just wrapper around *bun.DB
type BunAdapter struct {
	*bun.DB
	matcher MatcherOptions
}

func NewBunAdapter(bunConnection *bun.DB, opts ...func(*BunAdapter)) *BunAdapter {
	defaultMatcher := MatcherOptions{
		SchemaName: "public",
		TableName:  "casbin_policy",
		PType:      "ptype",
		V0:         "v0",
		V1:         "v1",
		V2:         "v2",
		V3:         "v3",
		V4:         "v4",
		V5:         "v5",
	}
	a := &BunAdapter{bunConnection, defaultMatcher}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

func WithMatcherOptions(matcher MatcherOptions) func(*BunAdapter) {
	return func(a *BunAdapter) {
		a.matcher = matcher
	}
}

// LoadPolicy loads all policy rules from the storage
func (a *BunAdapter) LoadPolicy(model model.Model) error {
	return errors.New("not implemented")
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
