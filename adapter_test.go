package casbinbunadapter

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// go test -run '^TestMatcherDefaults$' *.go -v
func TestMatcherDefaults(t *testing.T) {
	matcher := MatcherOptions{
		SchemaName: "dev",
		TableName:  "potato_policies",
		PType:      "pt",
		V0:         "v0",
		V1:         "haha",
		V2:         "",
		V3:         "v3",
		V4:         "v4",
	}
	adapter := NewBunAdapter(nil, WithMatcherOptions(matcher))
	assert.Equal(t, "dev", adapter.matcher.SchemaName)
	assert.Equal(t, "potato_policies", adapter.matcher.TableName)
	assert.Equal(t, defaultMatcherOpts.ID, adapter.matcher.ID)
	assert.Equal(t, "pt", adapter.matcher.PType)
	assert.Equal(t, "v0", adapter.matcher.V0)
	assert.Equal(t, "haha", adapter.matcher.V1)
	assert.Equal(t, defaultMatcherOpts.V2, adapter.matcher.V2) // Must be default
	assert.Equal(t, "v3", adapter.matcher.V3)
	assert.Equal(t, "v4", adapter.matcher.V4)
	assert.Equal(t, defaultMatcherOpts.V5, adapter.matcher.V5)
}

// go test -run '^TestTriggerDefaults$' *.go -v
func TestTriggerDefaults(t *testing.T) {
	trigger := TriggerOptions{
		FunctionName:   "user_fn",
		ChannelName:    "custom_ch_name",
		TriggerReplace: true,
	}
	adapter := NewBunAdapter(nil, WithTriggerOptions(trigger))
	assert.Equal(t, defaultTriggerOpts.Name, adapter.trigger.Name)
	assert.Equal(t, "user_fn", adapter.trigger.FunctionName) // Must be default
	assert.Equal(t, defaultTriggerOpts.FunctionSchemaName, adapter.trigger.FunctionSchemaName)
	assert.Equal(t, false, adapter.trigger.FunctionReplace)
	assert.Equal(t, true, adapter.trigger.TriggerReplace)
	assert.Equal(t, "custom_ch_name", adapter.trigger.ChannelName)
}
