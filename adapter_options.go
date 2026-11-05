package casbinbunadapter

var (
	defaultMatcherOpts = MatcherOptions{
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
)

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
