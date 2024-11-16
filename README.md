
# Casbin Bun Adapter for Postgres
# w.i.p

## Table of Contents
- [About](#about)
- [Installation](#installation)
- [Usage](#usage)


## About

It is just adapter for [Casbin](https://casbin.org/) using Golang ORM called [Bun](https://bun.uptrace.dev/).

This adapter supports listening to the policies update in database via [triggers](https://www.postgresql.org/docs/8.1/triggers.html), so when something is changed in database then your application would be aware of it.

__Attentions/warnings__:

- PostgreSQL is only supported database currently (since of heavy [trigger](https://www.postgresql.org/docs/8.1/triggers.html) feature usage)! As for other databases (MySQL, SQLite, Microsoft SQL Server) PRs are welcome.

- [AutoSave](https://casbin.org/docs/adapters/#autosave) feature is not implemented yet.

- Do not combine [StartUpdatesListening](./trigger.go#L103) and [SavePolicy](./adapter.go#L156) since it could cause infinite recursion. You should either update Casbin in-memory object with database table updates (via trigger) or update database table due Casbin in-memory updates (via direct method calls) but not both techniques same time.

- While using [StartUpdatesListening](./trigger.go#L103) _UPDATE_ operation on table calls [RemovePolicy/AddPolicy sequentially](./trigger.go#L186) without rollback mechanism. That means is AddPolicy call fails on `*casbin.SyncedEnforcer` then there is no rollback of previously called RemovePolicy

## Installation
```shell
go get github.com/LdDl/cusbin-bun-adapter
```

## Usage
@todo
