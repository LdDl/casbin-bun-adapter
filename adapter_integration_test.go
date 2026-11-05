package casbinbunadapter

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

const storageTestModel = `[request_definition]
r = sub, obj, act
[policy_definition]
p = sub, obj, act, eft
[role_definition]
g = _, _
[policy_effect]
e = some(where (p.eft == allow)) && !some(where (p.eft == deny))
[matchers]
m = g(r.sub, p.sub) && r.obj == p.obj && r.act == p.act
`

func prepareStorageTest(t *testing.T) *BunAdapter {
	t.Helper()
	dsn := os.Getenv("CASBIN_TEST_DSN")
	if dsn == "" {
		t.Skip("CASBIN_TEST_DSN is required for PostgreSQL tests")
	}
	nameBytes := make([]byte, 8)
	_, err := rand.Read(nameBytes)
	if err != nil {
		t.Fatal(err)
	}
	schema := "casbin_test_" + hex.EncodeToString(nameBytes)
	db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())
	t.Cleanup(func() {
		err := db.Close()
		if err != nil {
			t.Error(err)
		}
	})
	_, err = db.Exec("CREATE SCHEMA ?", bun.Name(schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, err := db.Exec("DROP SCHEMA ? CASCADE", bun.Name(schema))
		if err != nil {
			t.Error(err)
		}
	})
	_, err = db.Exec(`CREATE TABLE ?.policies (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		ptype varchar(64) NOT NULL,
		v0 varchar(64), v1 varchar(64), v2 varchar(64),
		v3 varchar(64), v4 varchar(64), v5 varchar(64),
		UNIQUE NULLS NOT DISTINCT (ptype, v0, v1, v2, v3, v4, v5)
	)`, bun.Name(schema))
	if err != nil {
		t.Fatal(err)
	}
	return NewBunAdapter(db, WithMatcherOptions(MatcherOptions{SchemaName: schema, TableName: "policies"}))
}

func storageTestExec(t *testing.T, adapter *BunAdapter, query string, args ...interface{}) {
	t.Helper()
	_, err := adapter.Exec(query, args...)
	if err != nil {
		t.Fatal(err)
	}
}

func storageTestCount(t *testing.T, adapter *BunAdapter, expected int) {
	t.Helper()
	var count int
	err := adapter.NewSelect().
		TableExpr("?.?", bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName)).
		ColumnExpr("count(*)").
		Scan(context.Background(), &count)
	if err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("policy count: got %d, want %d", count, expected)
	}
}

func TestPostgresRevocationSurvivesReload(t *testing.T) {
	for _, grouping := range []bool{false, true} {
		name := "policy"
		if grouping {
			name = "grouping"
		}
		t.Run(name, func(t *testing.T) {
			adapter := prepareStorageTest(t)
			storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES ('p','readers','data','read','allow'), ('g','alice','readers',NULL,NULL)`, bun.Name(adapter.matcher.SchemaName))
			m, err := model.NewModelFromString(storageTestModel)
			if err != nil {
				t.Fatal(err)
			}
			enforcer, err := casbin.NewEnforcer(m, adapter)
			if err != nil {
				t.Fatal(err)
			}
			allowed, err := enforcer.Enforce("alice", "data", "read")
			if err != nil || !allowed {
				t.Fatalf("initial permission: %v, %v", allowed, err)
			}
			if grouping {
				_, err = enforcer.RemoveGroupingPolicy("alice", "readers")
			} else {
				_, err = enforcer.RemovePolicy("readers", "data", "read", "allow")
			}
			if err != nil {
				t.Fatal(err)
			}
			err = enforcer.LoadPolicy()
			if err != nil {
				t.Fatal(err)
			}
			allowed, err = enforcer.Enforce("alice", "data", "read")
			if err != nil || allowed {
				t.Fatalf("revoked permission after reload: %v, %v", allowed, err)
			}
			storageTestCount(t, adapter, 1)
		})
	}
}

func TestPostgresDeleteUnusedFields(t *testing.T) {
	adapter := prepareStorageTest(t)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3,v4,v5) VALUES
		('p','alice','data','read','allow',NULL,NULL),
		('p','alice','data','read','allow','',''),
		('p','alice','data','read','allow',NULL,''),
		('p','alice','data','read','allow','tenant',NULL)`, bun.Name(adapter.matcher.SchemaName))
	err := adapter.RemovePolicy("p", "p", []string{"alice", "data", "read", "allow"})
	if err != nil {
		t.Fatal(err)
	}
	storageTestCount(t, adapter, 1)
	err = adapter.AddPolicy("p", "p", []string{"bob", "data", "read", "allow"})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.RemovePolicy("p", "p", []string{"bob", "data", "read", "allow"})
	if err != nil {
		t.Fatal(err)
	}
	storageTestCount(t, adapter, 1)
}

func TestPostgresFilterPreservesOtherPolicies(t *testing.T) {
	adapter := prepareStorageTest(t)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES
		('p','alice','data','read','allow'), ('p','bob','data','read','allow'), ('g','alice','readers',NULL,NULL)`, bun.Name(adapter.matcher.SchemaName))
	err := adapter.RemoveFilteredPolicy("p", "p", 6, "alice")
	if err == nil {
		t.Fatal("invalid filter accepted")
	}
	storageTestCount(t, adapter, 3)
	err = adapter.RemoveFilteredPolicy("p", "p", 0, "alice", "", "read")
	if err != nil {
		t.Fatal(err)
	}
	storageTestCount(t, adapter, 2)
	err = adapter.RemoveFilteredPolicy("p", "p", 0, "")
	if err != nil {
		t.Fatal(err)
	}
	storageTestCount(t, adapter, 1)
}

func TestPostgresSaveRollback(t *testing.T) {
	adapter := prepareStorageTest(t)
	storageTestExec(t, adapter, `INSERT INTO ?.policies (ptype,v0,v1,v2,v3) VALUES ('p','original','data','read','allow')`, bun.Name(adapter.matcher.SchemaName))
	m, err := model.NewModelFromString(storageTestModel)
	if err != nil {
		t.Fatal(err)
	}
	err = m.AddPolicy("p", "p", []string{"new", "data", "read", "allow"})
	if err != nil {
		t.Fatal(err)
	}
	err = m.AddPolicy("p", "p", []string{strings.Repeat("x", 65), "data", "read", "allow"})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.SavePolicy(m)
	if err == nil {
		t.Fatal("database constraint violation not reported")
	}
	storageTestCount(t, adapter, 1)
	var subject string
	err = adapter.NewSelect().
		TableExpr("?.?", bun.Name(adapter.matcher.SchemaName), bun.Name(adapter.matcher.TableName)).
		Column("v0").
		Scan(context.Background(), &subject)
	if err != nil || subject != "original" {
		t.Fatalf("original policy lost: %q, %v", subject, err)
	}
}
