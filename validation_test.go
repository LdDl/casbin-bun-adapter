package casbinbunadapter

import (
	"strings"
	"testing"

	"github.com/casbin/casbin/v2/model"
)

type policyValidationCase struct {
	name  string
	sec   string
	ptype string
	rule  []string
}

func TestPolicyValidationBeforeStorage(t *testing.T) {
	cases := []policyValidationCase{
		{name: "empty type", sec: "p", rule: []string{"alice"}},
		{name: "unsupported type", sec: "p", ptype: "x", rule: []string{"alice"}},
		{name: "mismatched section", sec: "g", ptype: "p", rule: []string{"alice"}},
		{name: "empty section", ptype: "p", rule: []string{"alice"}},
		{name: "empty rule", sec: "p", ptype: "p"},
		{name: "too many fields", sec: "p", ptype: "p", rule: []string{"1", "2", "3", "4", "5", "6", "7"}},
		{name: "NUL field", sec: "p", ptype: "p", rule: []string{"a\x00b"}},
		{name: "invalid UTF8", sec: "p", ptype: "p", rule: []string{string([]byte{0xff})}},
	}
	adapter := NewBunAdapter(nil)
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			err := adapter.AddPolicy(item.sec, item.ptype, item.rule)
			if err == nil {
				t.Fatal("invalid insert accepted")
			}
			err = adapter.RemovePolicy(item.sec, item.ptype, item.rule)
			if err == nil {
				t.Fatal("invalid delete accepted")
			}
		})
	}
}

type filterValidationCase struct {
	index  int
	values []string
}

func TestFilterValidationBeforeStorage(t *testing.T) {
	cases := []filterValidationCase{
		{index: -1, values: []string{"alice"}},
		{index: 6, values: []string{"alice"}},
		{index: 5, values: []string{"alice", "read"}},
		{index: 0, values: []string{"a\x00b"}},
	}
	adapter := NewBunAdapter(nil)
	for _, item := range cases {
		err := adapter.RemoveFilteredPolicy("p", "p", item.index, item.values...)
		if err == nil {
			t.Fatalf("invalid filter accepted: %+v", item)
		}
	}
}

func TestLoadInvalidPolicyType(t *testing.T) {
	for _, ptype := range []string{"", "x", "p\x00", string([]byte{0xff})} {
		err := loadSinglePolicy(CasbinPolicy{PType: ptype}, model.Model{})
		if err == nil {
			t.Fatalf("invalid policy type accepted: %q", ptype)
		}
	}
}

func TestSaveValidatesBeforeReplacingStorage(t *testing.T) {
	m := model.NewModel()
	m.AddDef("p", "p", "sub, obj, act")
	m["p"]["p"].Policy = [][]string{{"alice", "data", "read"}, strings.Split("1,2,3,4,5,6,7", ",")}
	err := NewBunAdapter(nil).SavePolicy(m)
	if err == nil {
		t.Fatal("overlong rule accepted")
	}
}

func TestSaveRejectsModelArityMismatch(t *testing.T) {
	m := model.NewModel()
	m.AddDef("p", "p", "sub, obj, act")
	m["p"]["p"].Policy = [][]string{{"alice", "data"}}
	err := NewBunAdapter(nil).SavePolicy(m)
	if err == nil {
		t.Fatal("rule with incorrect model arity accepted")
	}
}
