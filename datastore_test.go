package casbinbunadapter

import (
	"encoding/json"
	"reflect"
	"testing"
)

type ruleRoundTripCase struct {
	name  string
	ptype string
	rule  []string
}

func TestPolicyRuleRoundTrip(t *testing.T) {
	cases := []ruleRoundTripCase{
		{name: "middle empty", ptype: "p", rule: []string{"alice", "", "read", "allow"}},
		{name: "last empty", ptype: "p2", rule: []string{"alice", "data", "read", ""}},
		{name: "all empty", ptype: "g2", rule: []string{"", ""}},
		{name: "six fields", ptype: "p", rule: []string{"alice", "", "read", "", "allow", ""}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			policy, err := NewCasbinPolicyFrom(item.ptype, item.rule)
			if err != nil {
				t.Fatal(err)
			}
			payload, err := json.Marshal(policy)
			if err != nil {
				t.Fatal(err)
			}
			var restored CasbinPolicy
			err = json.Unmarshal(payload, &restored)
			if err != nil {
				t.Fatal(err)
			}
			rule, err := restored.getRuleDefinition()
			if err != nil {
				t.Fatal(err)
			}
			if restored.PType != item.ptype || !reflect.DeepEqual(rule, item.rule) {
				t.Fatalf("policy changed: %q %#v", restored.PType, rule)
			}
		})
	}
}

func TestPolicyConstructorOwnsValues(t *testing.T) {
	rule := []string{"alice", "data", "read"}
	policy, err := NewCasbinPolicyFrom("p", rule)
	if err != nil {
		t.Fatal(err)
	}
	rule[0] = "bob"
	if policy.V0 == nil || *policy.V0 != "alice" || policy.V3 != nil {
		t.Fatal("constructor aliases input or populates unused fields")
	}
}

func TestPolicyConstructorRejectsInvalidInput(t *testing.T) {
	_, err := NewCasbinPolicyFrom("", []string{"alice"})
	if err == nil {
		t.Fatal("empty policy type accepted")
	}
	_, err = NewCasbinPolicyFrom("p", []string{"1", "2", "3", "4", "5", "6", "7"})
	if err == nil {
		t.Fatal("overlong rule truncated")
	}
	_, err = NewCasbinPolicyFrom("g", nil)
	if err == nil {
		t.Fatal("empty rule accepted")
	}
}

func TestPolicyRejectsNullGap(t *testing.T) {
	policy, err := NewCasbinPolicyFrom("p", []string{"alice", "data", "read"})
	if err != nil {
		t.Fatal(err)
	}
	policy.V1 = nil
	_, err = policy.getRuleDefinition()
	if err == nil {
		t.Fatal("value after unused NULL accepted")
	}
}
