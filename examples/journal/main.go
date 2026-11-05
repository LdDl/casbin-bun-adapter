package main

import (
	"context"
	"database/sql"
	_ "embed"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	casbinbunadapter "github.com/LdDl/casbin-bun-adapter"
	"github.com/casbin/casbin/v2/model"
	"github.com/uptrace/bun"
	"github.com/uptrace/bun/dialect/pgdialect"
	"github.com/uptrace/bun/driver/pgdriver"
)

const journalSchema = "journal_example_log"

//go:embed schema.sql
var schemaSQL string

//go:embed rbac_deny.conf
var policyModel string

type configuration struct {
	Initialize bool
	ReaderRole string
	Address    string
}

func main() {
	var config configuration
	flag.BoolVar(&config.Initialize, "init", false, "Create the example policy table and journal as their owner")
	flag.StringVar(&config.ReaderRole, "reader-role", "", "Existing application database role for initialization")
	flag.StringVar(&config.Address, "addr", "127.0.0.1:8080", "HTTP listen address")
	flag.Parse()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err := run(ctx, config)
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, config configuration) error {
	dsn := os.Getenv("DATABASE_DSN")
	if dsn == "" {
		return fmt.Errorf("DATABASE_DSN is required")
	}
	db := bun.NewDB(sql.OpenDB(pgdriver.NewConnector(pgdriver.WithDSN(dsn))), pgdialect.New())
	defer db.Close()
	adapter := casbinbunadapter.NewBunAdapter(db, casbinbunadapter.WithMatcherOptions(casbinbunadapter.MatcherOptions{
		SchemaName: "journal_example",
		TableName:  "policies",
	}))
	if config.Initialize {
		return initialize(ctx, db, adapter, config.ReaderRole)
	}
	m, err := model.NewModelFromString(policyModel)
	if err != nil {
		return err
	}
	reader, err := adapter.NewJournalReader(ctx, journalSchema, m, casbinbunadapter.JournalReaderOptions{Listen: true})
	if err != nil {
		return err
	}
	return serve(ctx, reader, config.Address)
}

func initialize(ctx context.Context, db *bun.DB, adapter *casbinbunadapter.BunAdapter, readerRole string) error {
	if readerRole == "" {
		return fmt.Errorf("-reader-role is required with -init")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := db.RunInTx(ctx, nil, func(ctx context.Context, tx bun.Tx) error {
		_, err := tx.ExecContext(ctx, schemaSQL)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			GRANT USAGE ON SCHEMA journal_example TO ?;
			GRANT SELECT ON journal_example.policies TO ?;
		`, bun.Name(readerRole), bun.Name(readerRole))
		return err
	})
	if err != nil {
		return err
	}
	return adapter.CreateJournal(ctx, journalSchema, readerRole)
}
