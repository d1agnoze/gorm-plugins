# txtracker

Transaction depth tracking and post-outermost-commit hooks for GORM v2.

## Installation

```bash
go get gorm-plugins
```

Use the helpers directly; no `db.Use` registration is required.

```go
import (
    "gorm-plugins/txtracker"

    "gorm.io/driver/sqlite"
    "gorm.io/gorm"
)

db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
if err != nil {
    return err
}
```

## Usage

Use `BeginTransaction` instead of `db.Transaction` when you need transaction
depth tracking or post-commit hooks.

```go
err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
    if err := tx.Create(&User{Name: "alice"}).Error; err != nil {
        return err
    }

    if txtracker.IsOutermostTransaction(tx) {
        txtracker.OnCommit(tx, func() {
            publishUserCreated("alice")
        })
    }

    return nil
})
```

## API

- `BeginTransaction(db, fc, opts...)` wraps `db.Transaction()` and tracks nesting depth via `context.Context`.
- `OnCommit(db, fn)` registers a hook that runs only after the outermost tracked transaction commits successfully.
- `IsOutermostTransaction(db)` reports whether the current tracked depth is `1`. If tracking is absent, it returns `true`.
- `InTransaction(db)` reports whether the current context is inside `BeginTransaction`.
- `TransactionDepth(db)` returns the current tracked nesting depth.

Hooks execute synchronously in FIFO order after the outermost commit. If the outermost transaction rolls back, hooks are discarded. If a hook panics, later hooks do not run.

## Limitations

- `OnCommit` panics if called outside a `BeginTransaction` context.
- If you mix manual `db.Begin()` / `tx.Commit()` with `BeginTransaction`, tracking only covers the `BeginTransaction` portion. Hooks may run after a savepoint release rather than the manual outer commit.
- Hooks are tied to the outermost tracked commit, not individual savepoints. If an inner savepoint rolls back but the outer transaction later commits, hooks registered inside the rolled-back savepoint still run.
