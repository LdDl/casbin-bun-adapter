# Example of using PostgreSQL triggers to update enforcer in-memory data

__Attention__: It can be used with `*casbin.SyncedEnforcer` enforced only.

Prepare trigger options and then start goroutine and handle possible errors:

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

Calling `StartUpdatesListening` will create PostgreSQL trigger and SQL function for tracking changes in the database and then start listening to the database channel. For more details look at [trigger.go](../../trigger.go)
