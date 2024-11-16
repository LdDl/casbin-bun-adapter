package main

import (
	"crypto/tls"
	"database/sql"
	"fmt"
	"log"

	casbinbunadapter "github.com/LdDl/casbin-bun-adapter"
	"github.com/casbin/casbin/v2"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

func main() {
	/* Just database connection parameters */
	dbHost := "localhost"
	dbPort := 5432
	dbUser := "postgres"
	dbPassword := "postgres"
	dbName := "postgres"
	var tlsConf *tls.Config = nil
	appName := "example_custom_names"

	/* Initialize driver connector and get *bun.DB object */
	sqldb := sql.OpenDB(pgdriver.NewConnector(
		pgdriver.WithAddr(fmt.Sprintf("%s:%d", dbHost, dbPort)),
		pgdriver.WithUser(dbUser),
		pgdriver.WithPassword(dbPassword),
		pgdriver.WithDatabase(dbName),
		pgdriver.WithTLSConfig(tlsConf),
		pgdriver.WithApplicationName(appName),
	))
	dbConn := bun.NewDB(sqldb, pgdialect.New())
	defer func(db *bun.DB) {
		err := db.Close() // Make sure that you finalize connection to the database
		if err != nil {
			log.Println("Error on closing database connection", err)
		}
	}(dbConn)

	/* Define custom matcher */
	matcher := casbinbunadapter.MatcherOptions{
		SchemaName: "dev",
		TableName:  "potato_policies",
		ID:         "id",
		PType:      "pt",
		V0:         "v0",
		V1:         "haha",
		V2:         "",
		V3:         "v3",
		V4:         "v4",
		V5:         "v5",
	}
	/* Initialize adapter */
	adapter := casbinbunadapter.NewBunAdapter(dbConn, casbinbunadapter.WithMatcherOptions(matcher))
	enforcer, err := casbin.NewEnforcer("examples/custom_names/rbac_deny.conf", adapter)
	if err != nil {
		log.Println("Error on creating new casbin enforcer", err)
		return
	}
	_ = enforcer
}
