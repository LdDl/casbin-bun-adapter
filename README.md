
# Casbin Bun Adapter for Postgres

[![GoDoc](https://godoc.org/github.com/LdDl/casbin-bun-adapter?status.svg)](https://godoc.org/github.com/LdDl/casbin-bun-adapter) [![Go Report Card](https://goreportcard.com/badge/github.com/LdDl/casbin-bun-adapter)](https://goreportcard.com/report/github.com/LdDl/casbin-bun-adapter) [![GitHub tag](https://img.shields.io/github/tag/LdDl/casbin-bun-adapter.svg)](https://github.com/LdDl/casbin-bun-adapter/releases)

## Table of Contents
- [About](#about)
- [Installation](#installation)
- [Usage](#usage)


## About

It is just adapter for [Casbin](https://casbin.org/) using Golang ORM called [Bun](https://bun.uptrace.dev/).

This adapter supports listening to the policies update in database via [triggers](https://www.postgresql.org/docs/8.1/triggers.html), so when something is changed in database then your application would be aware of it.

[AutoSave](https://casbin.org/docs/adapters/#autosave) feature is implemented.

Supported
__Attentions/warnings__:

- PostgreSQL is only supported database currently (since of heavy [trigger](https://www.postgresql.org/docs/8.1/triggers.html) feature usage)! As for other databases (MySQL, SQLite, Microsoft SQL Server) PRs are welcome.

- This repository is not pretend to be the best Casbin adapter, but it works for my use-cases. Check out others implementations [here](https://casbin.org/docs/adapters/#supported-adapters)

- Do not combine [StartUpdatesListening](./trigger.go) and [SavePolicy](./adapter.go) (or `AutoSave` feature) since it could cause infinite recursion. You should either update Casbin in-memory object with database table updates (via trigger) or update database table due Casbin in-memory updates (via direct method calls) but not both techniques same time.

- While using [StartUpdatesListening](./trigger.go) _UPDATE_ operation on table calls [RemovePolicy/AddPolicy sequentially](./policy_change.go) without rollback mechanism. That means if AddPolicy call fails on `*casbin.SyncedEnforcer` then there will not be any rollback for previously called RemovePolicy


## Installation
```shell
go get github.com/LdDl/casbin-bun-adapter
```

## Usage

There are three examples how to use it:
1. Plain example without AutoSave or PostgreSQL triggers involed - [./examples/custom_names](./examples/custom_names/main.go)
2. Example with using AutoSave feature - [./examples/autosave_changes](./examples/autosave_changes/main.go). Just add this line after `*casbin.Enforcer` is initialized:
    ```go
    // ...
    enforcer.EnableAutoSave(true)
    // ...
    ```
3. Example with using PostgreSQL (version 14.x and above) triggers feature - [./examples/listen_changes](./examples/listen_changes/main.go). It can be used with `*casbin.SyncedEnforcer` only:
    ```go
    // ...
    trigger := casbinbunadapter.TriggerOptions{
        Name:               "casbin_call_trigger",
        FunctionName:       "update_policies_table",
        FunctionSchemaName: "public",
        FunctionReplace:    true,
        TriggerReplace:     true, // Works only for PostgreSQL 14.x and above
        ChannelName:        "CASBIN_UPDATE_MESSAGE",
    }
    adapter := casbinbunadapter.NewBunAdapter(
        dbConn,
        casbinbunadapter.WithMatcherOptions(matcher),
        casbinbunadapter.WithTriggerOptions(trigger),
    )
    // ...
	enforcer.EnableAutoSave(false) // Explicit disable
    errCh := make(chan error)
    go func(enf *casbin.SyncedEnforcer, errCh chan error) {
        err = adapter.StartUpdatesListening(enf)
        if err != nil {
            log.Println("Error on database listener", err)
            errCh <- err
        }
    }(enforcer, errCh)
    // ...
    select {
	case e := <-errCh:
		log.Println("Err", e)
		return
	}
    ```

## Storage

Rules support six fields (`v0` through `v5`, type `*string`). NULL marks unused columns; empty strings retain their positions. Existing empty padding requires normalization to NULL according to the model.

`NewCasbinPolicyFrom` returns `(CasbinPolicy, error)`. Empty filter values are wildcards; an all-wildcard filter removes every rule of the specified type.

## Transactional journal

`adapter.CreateJournal(ctx, "casbin_journal", "app_user")` installs a journal once, as the policy table owner. It requires a new schema and PostgreSQL 14+ with a permanent, non-partitioned policy table without inheritance. Installation locks the policy table.

`changes` records INSERT, UPDATE, DELETE and TRUNCATE; `batches` groups them by transaction. `state.revision` identifies the committed head. Rollbacks leave no records. Policy writers run sequentially until commit; long transactions delay other writers.

Pass existing application database role names after the schema name. The installer grants journal read access automatically and rejects owner or administrative roles. Without role names, no reader grants are issued. Roles, passwords and policy table permissions remain environment configuration; unexpected default grants abort installation.

The journal records changes without LISTEN or enforcer updates. Existing policies are not copied; automatic cleanup is absent. `StartUpdatesListening` operates independently.

## Tests

```shell
go test ./...
CASBIN_TEST_DSN='postgres://localhost/casbin_test?sslmode=disable' go test ./...
```

Database tests require PostgreSQL 15+ and schema creation permission in a dedicated test database. Journal privilege tests also require role creation and switching permissions. Without `CASBIN_TEST_DSN`, database tests are skipped.
