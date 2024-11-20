
# Casbin Bun Adapter for Postgres
# w.i.p - first v0.0.1 release will be made after dealing with some AutoSave features and adapter tests

## Table of Contents
- [About](#about)
- [Installation](#installation)
- [Usage](#usage)


## About

It is just adapter for [Casbin](https://casbin.org/) using Golang ORM called [Bun](https://bun.uptrace.dev/).

This adapter supports listening to the policies update in database via [triggers](https://www.postgresql.org/docs/8.1/triggers.html), so when something is changed in database then your application would be aware of it.

[AutoSave](https://casbin.org/docs/adapters/#autosave) feature is implemented.

__Attentions/warnings__:

- PostgreSQL is only supported database currently (since of heavy [trigger](https://www.postgresql.org/docs/8.1/triggers.html) feature usage)! As for other databases (MySQL, SQLite, Microsoft SQL Server) PRs are welcome.

- This repository is not pretend to be the best Casbin adapter, but it works for my use-cases. Check out others implementations [here](https://casbin.org/docs/adapters/#supported-adapters)

- Do not combine [StartUpdatesListening](./trigger.go#L159) and [SavePolicy](./adapter.go#L158) since it could cause infinite recursion. You should either update Casbin in-memory object with database table updates (via trigger) or update database table due Casbin in-memory updates (via direct method calls) but not both techniques same time.

- While using [StartUpdatesListening](./trigger.go#L159) _UPDATE_ operation on table calls [RemovePolicy/AddPolicy sequentially](./trigger.go#L186) without rollback mechanism. That means if AddPolicy call fails on `*casbin.SyncedEnforcer` then there will not be any rollback for previously called RemovePolicy


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