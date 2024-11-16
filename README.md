
# Casbin Bun Adapter for Postgres
# w.i.p

## Table of Contents
- [About](#about)
- [Installation](#installation)
- [Usage](#usage)


## About

It is just adapter for [Casbin](https://casbin.org/) using Golang ORM called [Bun](https://bun.uptrace.dev/).

This adapter supports listening to the policies update in database via [triggers](https://www.postgresql.org/docs/8.1/triggers.html), so when something is changed in database then your application would be aware of it.

__Attention__: PostgreSQL is only supported database currently! As for other databases (MySQL, SQLite, Microsoft SQL Server) PRs are welcome.


## Installation
```shell
go get github.com/LdDl/cusbin-bun-adapter
```

## Usage
@todo
