# Example of using [AutoSave](#https://casbin.org/docs/adapters/#autosave) feature

Just enable it for the `*casbin.Enforcer` object:
```go
// ...
enforcer.EnableAutoSave(true)
// ...
```