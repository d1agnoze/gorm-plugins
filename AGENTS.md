# AGENTS.md

## Project overview

Collection of behavioral plugins for GORM v2 (transaction hooks, post-commit hooks, etc.).
Each plugin lives in its own Go package under the repo root.
Plugins modify GORM's callback pipeline — they do **not** implement drivers or dialects.

## Source of truth

- **Primary**: official GORM v2 docs at https://gorm.io/docs/
- **API reference**: https://pkg.go.dev/gorm.io/gorm
- **Plugin authoring**: https://gorm.io/docs/write_plugins.html
- **Callback internals**: https://github.com/go-gorm/gorm/blob/master/callbacks/callbacks.go
- **GORM v1 is dead** — never reference `github.com/jinzhu/gorm` APIs, patterns, or workarounds. The v2 import path is `gorm.io/gorm`.

## GORM v2 plugin contract

Every plugin must implement the `gorm.Plugin` interface:

```go
type Plugin interface {
    Name() string
    Initialize(*gorm.DB) error
}
```

Register with `db.Use(plugin)`. Callbacks are registered on the global `*gorm.DB`, not per-session.

## Callback pipeline (critical to understand)

The default callback chain for **Create** is:

```
gorm:begin_transaction → gorm:before_create → gorm:save_before_associations →
gorm:create → gorm:save_after_associations → gorm:after_create →
gorm:commit_or_rollback_transaction
```

Similar chains exist for Update, Delete, Query. Use `.Before()` / `.After()` to position custom callbacks relative to these named slots. Example:

```go
db.Callback().Create().After("gorm:after_create").Register("myplugin:post_create", myFunc)
```

Callback functions have signature `func(*gorm.DB)`. Access statement, schema, context via `db.Statement`.

## Key v2 gotchas an agent will hit

- **Hooks vs. Callbacks**: Model hooks (`BeforeCreate`, `AfterCreate`, etc.) run _inside_ the default transaction. Callbacks registered via `db.Callback()` run in the callback chain and can be positioned outside the transaction boundary.
- **`db.Statement.Schema`** may be nil for raw SQL — always nil-check before accessing fields/relationships.
- **`db.Statement.ReflectValue`** can be a slice, array, or struct — handle all `reflect.Kind` cases.
- **Session safety**: `*gorm.DB` methods return a new session. Callbacks receive the session `*gorm.DB`, not the global one. Do not store mutable state on it between calls.
- **`SkipHooks` session option** can disable model hooks — your callback-based plugin still runs unless explicitly removed.
- **`SkipDefaultTransaction`**: when true, `gorm:begin_transaction` and `gorm:commit_or_rollback_transaction` callbacks are skipped. If your plugin hooks after commit, this changes behavior.
- **Error propagation**: set `db.AddError(err)` inside callbacks. Returning an error from a model hook rolls back the transaction.
- **Context**: always use `db.Statement.Context` (not `context.Background()`).

## Project structure

```
<project-root>/
  AGENTS.md             # This file — repo-wide agent instructions
  README.md             # Lists all plugins with summaries; links to plugin READMEs
  *.go                  # Shared helpers/utilities used across plugins
  go.mod                # Root module for the entire plugin collection

  <plugin-name>/
    <plugin-name>.go      # Plugin struct + Initialize
    <plugin-name>_test.go
    README.md             # User-facing: installation, usage, config options
    AGENTS.md             # Plugin-specific agent instructions
```

### Key rules

- Each plugin lives in its own directory and Go package at the repo root (e.g., `pluginA/`).
- Shared functions, helpers, and utilities go in `<project-root>/*.go` files.
- All plugins share the root `go.mod`. Do not create per-plugin `go.mod` or `go.sum` files unless the user explicitly asks for a separate module.
- Add plugin dependencies to the root module and run `go mod tidy` from the repo root after adding or removing imports.

### Documentation ownership

- **`<project-root>/README.md`**: catalog of all plugins — name, one-line summary, link to the plugin's own README. Keep it updated when adding/removing plugins.
- **`<plugin-name>/README.md`**: user-facing docs — installation, usage examples, configuration reference. This is what plugin consumers read. Installation examples should assume consumers install the root module, not an individual plugin submodule.
- **`<plugin-name>/AGENTS.md`**: plugin-specific agent instructions (e.g., "for plugin A, it should do X"). When the user gives instructions targeting a specific plugin, write them here, not in the root `AGENTS.md`.
- **Go doc comments** in source code: for project developers/maintainers. Every exported type, function, and method must have a doc comment. Follow the [Go Doc Comments](https://go.dev/doc/comment) spec so documentation renders correctly on [pkg.go.dev](https://pkg.go.dev/about#best-practices):
  - Package comment starts with `// Package <name> ...` and summarizes purpose in the first sentence (shown in pkg.go.dev search results and package lists).
  - Exported symbol comments start with the symbol name: `// MyFunc does ...`.
  - Use blank comment lines to separate paragraphs; indent lines for preformatted blocks.
  - Use `// Deprecated: ...` to mark deprecated symbols.
  - For large package docs, use a dedicated `doc.go` file containing only the package comment and `package` clause.

## Development commands

```bash
# Run all tests for a plugin
go test ./<plugin-name>/...

# Run a single test
go test ./<plugin-name>/... -run TestSpecificName -v

# Vet and lint
go vet ./<plugin-name>/...

# Tidy deps after adding/removing imports in the root module
go mod tidy
```

## Testing conventions

- Use SQLite (`gorm.io/driver/sqlite`) for unit tests — no external services needed.
- For integration tests needing MySQL/PostgreSQL, use build tags or env vars to opt in. Document any required env vars in the plugin's README.
- Always open a fresh `*gorm.DB` per test (or per subtest) to avoid callback bleed between tests.
- Run `db.AutoMigrate(&Model{})` in test setup to create tables.
- Test both `SkipDefaultTransaction: false` (default) and `SkipDefaultTransaction: true` if the plugin interacts with the transaction lifecycle.
- Test with `DryRun: true` session when asserting generated SQL without DB side effects.
- GORM's own test models use `gorm.Model` (ID, CreatedAt, UpdatedAt, DeletedAt). Follow the same pattern for test fixtures.

### Test skeleton

```go
package myplugin_test

import (
    "testing"
    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
)

func setupTestDB(t *testing.T) *gorm.DB {
    t.Helper()
    db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
    if err != nil {
        t.Fatalf("failed to open db: %v", err)
    }
    return db
}

func TestPluginRegistration(t *testing.T) {
    db := setupTestDB(t)
    err := db.Use(&MyPlugin{})
    if err != nil {
        t.Fatalf("plugin registration failed: %v", err)
    }
    if _, ok := db.Config.Plugins["myplugin"]; !ok {
        t.Fatal("plugin not found in config")
    }
}
```

## Style

- Plugin names: lowercase, hyphenated for the package directory, camelCase for the Go type.
- Callback names: `<plugin-name>:<action>` (e.g., `postcommit:after_commit`).
- Exported config struct: `<PluginName>Config` or use functional options.
- Keep each plugin focused on one behavioral concern.
- Minimum Go version: match GORM v2's `go 1.18` requirement.

## Things to never do

- Reference `github.com/jinzhu/gorm` (GORM v1).
- Use `db.Exec` / `db.Raw` inside callbacks when GORM's clause builder can do the job.
- Mutate the global `*gorm.DB` config from inside a per-request callback.
- Assume `db.Statement.Schema` is non-nil.
- Skip error checks on `db.Use()` — it returns an error if `Initialize` fails.
