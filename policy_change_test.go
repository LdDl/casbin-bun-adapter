package casbinbunadapter

import (
	"testing"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

const namedPolicyTestModel = `[request_definition]
r = sub, obj, act
[policy_definition]
p = sub, obj, act, eft
p2 = sub, obj, act, eft
[role_definition]
g = _, _
g2 = _, _
[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))
[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj && r.act == p.act
`

func newNamedTestEnforcer(t *testing.T) *casbin.SyncedEnforcer {
	t.Helper()
	m, err := model.NewModelFromString(namedPolicyTestModel)
	if err != nil {
		t.Fatal(err)
	}
	enforcer, err := casbin.NewSyncedEnforcer(m)
	if err != nil {
		t.Fatal(err)
	}
	enforcer.EnableAutoSave(false)
	return enforcer
}

func makeTestPolicy(t *testing.T, ptype string, rule ...string) CasbinPolicy {
	t.Helper()
	policy, err := NewCasbinPolicyFrom(ptype, rule)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

func requireNamedRule(t *testing.T, enforcer *casbin.SyncedEnforcer, ptype string, rule []string, expected bool) {
	t.Helper()
	var found bool
	var err error
	if ptype[0] == 'p' {
		found, err = enforcer.HasNamedPolicy(ptype, rule)
	} else {
		found, err = enforcer.HasNamedGroupingPolicy(ptype, rule)
	}
	if err != nil || found != expected {
		t.Fatalf("rule %q %#v: found=%v, want=%v, error=%v", ptype, rule, found, expected, err)
	}
}

func TestNamedPolicyChanges(t *testing.T) {
	cases := []ruleRoundTripCase{
		{name: "policy", ptype: "p2", rule: []string{"alice", "", "read", "allow"}},
		{name: "grouping", ptype: "g2", rule: []string{"alice", ""}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			enforcer := newNamedTestEnforcer(t)
			oldPolicy := makeTestPolicy(t, item.ptype, item.rule...)
			err := applyPolicyChange(enforcer, TriggerDataPayload{EventType: EVENT_PAYLOAD_INSERT, New: oldPolicy})
			if err != nil {
				t.Fatal(err)
			}
			requireNamedRule(t, enforcer, item.ptype, item.rule, true)
			requireNamedRule(t, enforcer, item.ptype[:1], item.rule, false)
			updated := append([]string(nil), item.rule...)
			updated[0] = "bob"
			newPolicy := makeTestPolicy(t, item.ptype, updated...)
			err = applyPolicyChange(enforcer, TriggerDataPayload{EventType: EVENT_PAYLOAD_UPDATE, Old: oldPolicy, New: newPolicy})
			if err != nil {
				t.Fatal(err)
			}
			requireNamedRule(t, enforcer, item.ptype, item.rule, false)
			requireNamedRule(t, enforcer, item.ptype, updated, true)
			err = applyPolicyChange(enforcer, TriggerDataPayload{EventType: EVENT_PAYLOAD_DELETE, Old: newPolicy})
			if err != nil {
				t.Fatal(err)
			}
			requireNamedRule(t, enforcer, item.ptype, updated, false)
		})
	}
}

func TestInvalidChangeKeepsExistingRule(t *testing.T) {
	enforcer := newNamedTestEnforcer(t)
	old := makeTestPolicy(t, "p2", "alice", "data", "read", "deny")
	err := applyPolicyChange(enforcer, TriggerDataPayload{EventType: EVENT_PAYLOAD_INSERT, New: old})
	if err != nil {
		t.Fatal(err)
	}
	invalid := makeTestPolicy(t, "p2", "alice", "data", "read")
	err = applyPolicyChange(enforcer, TriggerDataPayload{EventType: EVENT_PAYLOAD_UPDATE, Old: old, New: invalid})
	if err == nil {
		t.Fatal("invalid model arity accepted")
	}
	requireNamedRule(t, enforcer, "p2", []string{"alice", "data", "read", "deny"}, true)
	err = applyPolicyChange(enforcer, TriggerDataPayload{EventType: EVENT_PAYLOAD_INSERT})
	if err == nil {
		t.Fatal("empty policy accepted")
	}
	err = applyPolicyChange(enforcer, TriggerDataPayload{EventType: "unknown", New: old})
	if err == nil {
		t.Fatal("unknown event type accepted")
	}
}
