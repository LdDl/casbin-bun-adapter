
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

__Attentions/warnings__:

- PostgreSQL is only supported database currently (since of heavy [trigger](https://www.postgresql.org/docs/8.1/triggers.html) feature usage)! As for other databases (MySQL, SQLite, Microsoft SQL Server) PRs are welcome.

- This repository is not pretend to be the best Casbin adapter, but it works for my use-cases. Check out others implementations [here](https://casbin.org/docs/adapters/#supported-adapters)

## Installation

```shell
go get github.com/LdDl/casbin-bun-adapter
```

## Usage

Examples:

1. Plain example without AutoSave or PostgreSQL triggers involed - [./examples/custom_names](./examples/custom_names/main.go)
2. Example with using AutoSave feature - [./examples/autosave_changes](./examples/autosave_changes/main.go). Just add this line after `*casbin.Enforcer` is initialized:
    ```go
    // ...
    enforcer.EnableAutoSave(true)
    // ...
    ```
3. Transactional journal with strict checks and background synchronization - [./examples/journal](./examples/journal/README.md).

## Storage

Rules support six fields (`v0` through `v5`, type `*string`). NULL marks unused columns; empty strings retain their positions. Existing empty padding requires normalization to NULL according to the model.

`NewCasbinPolicyFrom` returns `(CasbinPolicy, error)`. Empty filter values are wildcards; an all-wildcard filter removes every rule of the specified type.

## Transactional journal

Policies can be written through SQL or a Casbin enforcer using `BunAdapter`. Authorization uses a separate `JournalReader`. `SavePolicy` replaces the entire policy table.

Requires PostgreSQL 14+, a primary connection, unique policy rows and a permanent table without inheritance or partitions. Policy writers are serialized until commit; `PREPARE TRANSACTION` is unsupported.

The table owner creates the journal in a new schema and grants journal access to an existing application role:

```go
err := adapter.CreateJournal(ctx, "casbin_journal", "app_user")
if err != nil {
    return err
}
```

The application role also needs `SELECT` on the policy table. Using its connection:

```go
reader, err := adapter.NewJournalReader(ctx, "casbin_journal", policyModel,
    casbinbunadapter.JournalReaderOptions{Listen: true})
if err != nil {
    return err
}

allowed, err := reader.EnforceContext(ctx, "alice", "/reports", "read")
if err != nil {
    return err
}
if !allowed {
    return errors.New("access denied")
}
```

Each check reads the journal position and synchronizes before deciding. Synchronization errors deny access with `ErrJournalNotReady`; the default timeout is five seconds. Small changes apply incrementally; large changes or missing history reload a consistent snapshot.

A background worker receives notifications and removes journal history older than seven days:

```go
err := reader.Run(ctx)
if err != nil && !errors.Is(err, context.Canceled) {
    return err
}
```

`Run` blocks until cancellation. The [runnable example](./examples/journal/README.md) includes startup and shutdown.

| Option or method | Behavior |
| --- | --- |
| `Listen: true` | Dedicated pgdriver connection; direct PostgreSQL or session pooling. |
| `PollInterval` | Optional polling, disabled by default. |
| `EnforceCached` | No database request; may miss changes for up to `MaxStaleness` (default 30 seconds). |
| `Status()` | Synchronization, listener and cleanup errors. |
| `Sync(ctx)` | Manual synchronization; strict checks also work without `Run`. |

Transaction pooling cannot be used for LISTEN. Standard models keep a canonical policy set and two enforcers in memory. Custom setup belongs in `Configure`; mutable state must remain private to each enforcer.

## Tests

```shell
go test ./...
CASBIN_TEST_DSN='postgres://localhost/casbin_test?sslmode=disable' go test ./...
CASBIN_TEST_DSN='postgres://localhost/casbin_test?sslmode=disable' go test -run '^$' -bench PostgresJournal -benchtime=3x -benchmem
```

Database tests require PostgreSQL 15+ and schema creation permission in a dedicated test database. Journal privilege tests also require role creation and switching permissions. Without `CASBIN_TEST_DSN`, database tests are skipped.
