
# Casbin Bun Adapter for Postgres

[![GoDoc](https://godoc.org/github.com/LdDl/casbin-bun-adapter?status.svg)](https://godoc.org/github.com/LdDl/casbin-bun-adapter) [![Go Report Card](https://goreportcard.com/badge/github.com/LdDl/casbin-bun-adapter)](https://goreportcard.com/report/github.com/LdDl/casbin-bun-adapter) [![GitHub tag](https://img.shields.io/github/tag/LdDl/casbin-bun-adapter.svg)](https://github.com/LdDl/casbin-bun-adapter/releases)

## Table of Contents

- [About](#about)
- [Installation](#installation)
- [Usage](#usage)
- [Storage](#storage)
- [Transactional journal](#transactional-journal)
- [Tests](#tests)


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
        // Works only for PostgreSQL 14.x and above
        TriggerReplace:     true,
        ChannelName:        "CASBIN_UPDATE_MESSAGE",
    }
    adapter := casbinbunadapter.NewBunAdapter(
        dbConn,
        casbinbunadapter.WithMatcherOptions(matcher),
        casbinbunadapter.WithTriggerOptions(trigger),
    )
    // ...
    // Explicit disable
    enforcer.EnableAutoSave(false)
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

Requires PostgreSQL 14+, a new journal schema and a permanent policy table without inheritance or partitions. During migrations, the table owner installs the journal and grants access to an existing application role:

```go
err := adapter.CreateJournal(ctx, "casbin_journal", "app_user")
```

The application uses its separate database role:

```go
reader, err := adapter.NewJournalReader(ctx, "casbin_journal", m, casbinbunadapter.JournalReaderOptions{})
```

After checking `err`, authorize through `reader.EnforceContext(ctx, ...)` or `reader.Enforce(...)`. Each call reads the journal position from the primary PostgreSQL database and catches up to that position before evaluating access. Later writes do not extend its target. Synchronization failures return `ErrJournalNotReady`; context errors remain available through `errors.Is`. The default timeout is five seconds, including waiting for another updater.

`EnforceCached(...)` explicitly uses only local policies and can miss recent changes. It rejects synchronization errors, known unfinished catch-up and policies older than `MaxStaleness` (thirty seconds by default).

`Run(ctx)` maintains history retention. Background policy polling is disabled by default; a positive `PollInterval` enables it. Strict checks work without `Run`, including after it stops. History older than seven days is cleaned in portions of up to 1000 complete transactions. Readers reload policies if needed history is gone. `Status()` exposes synchronization and cleanup errors.

Policy rows must be unique. Writers are serialized until commit; atomic publication rebuilds the model and role graph in memory. `Configure` callbacks must not share mutable enforcer state.

For journals without cleanup support, the owner calls `EnableJournalCleanup(ctx, schema, appRole)` once. Manual scheduling uses `Sync(ctx)` and `PruneJournal(ctx, schema)`.

Do not combine the journal reader with `StartUpdatesListening`.

## Tests

```shell
go test ./...
CASBIN_TEST_DSN='postgres://localhost/casbin_test?sslmode=disable' go test ./...
```

Database tests require PostgreSQL 15+ and schema creation permission in a dedicated test database. Journal privilege tests also require role creation and switching permissions. Without `CASBIN_TEST_DSN`, database tests are skipped.
