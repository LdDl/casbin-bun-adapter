package casbinbunadapter

import (
	"github.com/uptrace/bun"
)

// CasbinPolicy is database storage format following the below
// https://casbin.org/docs/policy-storage#database-storage-format
// Template on which Golang struct has been prepared:
// CREATE TABLE casbin_policies (
//
//	  id int4 GENERATED BY DEFAULT AS IDENTITY( INCREMENT BY 1 MINVALUE 1 MAXVALUE 2147483647 START 1 CACHE 1 NO CYCLE) NOT NULL,
//		ptype varchar(2) DEFAULT 'p'::character varying NOT NULL,
//		v0 varchar(256) null,
//		v1 varchar(256) null,
//		v2 varchar(256) null,
//		v3 varchar(256) null,
//		v4 varchar(256) null,
//		v5 varchar(256) null,
//		CONSTRAINT casbin_policies_pk PRIMARY KEY (id),
//		CONSTRAINT casbin_policies_unique UNIQUE NULLS NOT DISTINCT (ptype, v0, v1, v2, v3, v4, v5) -- Aware! This will work only for Postgres 15+. For the ref. see: https://stackoverflow.com/a/8289253/6026885
//
// );
type CasbinPolicy struct {
	bun.BaseModel `bun:"casbin_policies,alias:t"`
	ID            int    `bun:"id,pk,autoincrement" json:"id"` // I'm not sure if 'autoincrement' will work as IDENTITY rather than SERIAL
	PType         string `bun:"ptype,type:varchar(2),notnull,default:'p'" json:"ptype"`
	V0            string `bun:"v0,type:varchar(256),nullzero" json:"v0"`
	V1            string `bun:"v1,type:varchar(256),nullzero" json:"v1"`
	V2            string `bun:"v2,type:varchar(256),nullzero" json:"v2"`
	V3            string `bun:"v3,type:varchar(256),nullzero" json:"v3"`
	V4            string `bun:"v4,type:varchar(256),nullzero" json:"v4"`
	V5            string `bun:"v5,type:varchar(256),nullzero" json:"v5"`
}

// MatcherOptions is for matching user defined columns to canonical Casbin columns
type MatcherOptions struct {
	SchemaName string
	TableName  string
	ID         string
	PType      string
	V0         string
	V1         string
	V2         string
	V3         string
	V4         string
	V5         string
}

// TriggerOptions is for defining trigger whicl will be executed after data update in database table
type TriggerOptions struct {
	// Trigger name
	Name string
	// Function which must be executed
	FunctionName string
	// Schema name where function is located
	FunctionSchemaName string
	// If function needed to be replaced in case it exists
	FunctionReplace bool
	// If trigger needed to be replaced in case it exists. Be carefull. This is PostgreSQL 14.x and above feature!
	TriggerReplace bool
	// Name for PostgreSQL channel for listening updates
	ChannelName string
}

func (cp CasbinPolicy) getRuleDefinition() []string {
	ans := make([]string, 0, 6)
	if cp.V0 != "" {
		ans = append(ans, cp.V0)
	}
	if cp.V1 != "" {
		ans = append(ans, cp.V1)
	}
	if cp.V2 != "" {
		ans = append(ans, cp.V2)
	}
	if cp.V3 != "" {
		ans = append(ans, cp.V3)
	}
	if cp.V4 != "" {
		ans = append(ans, cp.V4)
	}
	if cp.V5 != "" {
		ans = append(ans, cp.V5)
	}
	return ans
}

// NewCasbinPolicyFrom creates CasbinPolicy object from well-defined policy type and rules
func NewCasbinPolicyFrom(ptype string, rule []string) CasbinPolicy {
	cp := CasbinPolicy{
		PType: ptype,
	}
	for i := range rule {
		val := rule[i]
		// Rule is restricted by max 6 values in rule definition
		switch i {
		case 0:
			cp.V0 = val
		case 1:
			cp.V1 = val
		case 2:
			cp.V2 = val
		case 3:
			cp.V3 = val
		case 4:
			cp.V4 = val
		case 5:
			cp.V5 = val
		}
	}
	return cp
}
