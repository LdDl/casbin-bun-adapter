# Transactional journal example

The example exposes a local HTTP endpoint backed by `JournalReader`. Each request performs a strict authorization check. `Run` maintains the listener and journal retention; cancellation stops it before the database connection closes.

PostgreSQL 15+ is required for the example's unique constraint. The database and a separate application login role must exist. From the repository root, initialize the example schemas once as their owner:

```shell
DATABASE_DSN='postgres://owner:password@localhost/casbin_test?sslmode=disable' go run ./examples/journal -init -reader-role app_user
```

Start the application with the reader's credentials:

```shell
DATABASE_DSN='postgres://app_user:password@localhost/casbin_test?sslmode=disable' go run ./examples/journal
curl -i 'http://127.0.0.1:8080/check?sub=alice&obj=data1&act=read'
```

The request supplies the subject for this demonstration. Application authentication supplies the subject in an actual authorization handler. Responses are 200 for permission granted, 403 for permission denied and 503 for an authorization error.

To revoke the grant, execute as the policy table owner:

```sql
DELETE FROM journal_example.policies
WHERE ptype = 'g' AND v0 = 'alice' AND v1 = 'readers';
```

The next request returns 403 after this transaction commits. Restore the same grouping rule to grant access again. Ctrl+C stops the example.
