# txtracker — Agent Instructions

## Purpose

`txtracker` is a GORM v2 plugin that tracks transaction nesting depth via `context.Context`. It provides a wrapper around `db.Transaction()` and exposes query functions so callbacks and application code can determine whether they are inside the outermost transaction.

## Why this plugin exists

GORM v2 has no mechanism to track transaction nesting depth. The only detection is a binary type assertion (`ConnPool.(TxCommitter)` — in a tx or not). This causes a concrete problem:

- A callback registered `.After("gorm:commit_or_rollback_transaction")` fires after the **per-operation** commit callback, not after the outermost `db.Transaction()` commit.
- Inside a nested transaction, `gorm:commit_or_rollback_transaction` is a no-op (it checks `InstanceGet("gorm:started_transaction")` which was never set because `BeginTransaction` silently swallowed `ErrInvalidTransaction`). The callback fires but no real commit happened.
- There is no way for that callback to know whether the data is actually committed to disk or an outer transaction still holds it.

`txtracker` solves this by injecting a shared atomic depth counter into `context.Context` and providing a `BeginTransaction()` wrapper that fires post-commit hooks only after the outermost transaction completes.

## Key design decisions

1. **Context, not Settings**: `Statement.Settings` (`sync.Map`) is NOT reliable — it gets dropped on `clone == 1` paths (which happen during `Begin()` on root DB). `context.Context` is the only state carrier GORM reliably propagates through every code path (`getInstance()`, `Begin()`, `Session()`, nested `Transaction()`, and into callbacks).

2. **Shared `*int32` pointer, not immutable context values**: If we stored `depth int` as an immutable value, each nested `context.WithValue` would shadow the parent. By storing a `*int32` (pointer to atomic), all nesting levels share the same counter.

3. **Wrapper function, not callback replacement**: The plugin does NOT replace `gorm:commit_or_rollback_transaction` or any built-in callback. Users opt in by calling `txtracker.BeginTransaction()` instead of `db.Transaction()`.

4. **Default-true for `IsOutermostTransaction` when no tracking is active**: When there is no `*txState` in the context (e.g., a plain `db.Create()` without `BeginTransaction()`), `IsOutermostTransaction` returns `true`. This is correct because GORM's auto-wrap transaction has already committed by the time a post-commit callback fires.

## Implementation rules

- The plugin struct `TxTracker` implements `gorm.Plugin` with a no-op `Initialize()`. It registers no callbacks.
- All state is in `context.Context` via an unexported key type (`txCtxKey struct{}`).
- The context value is `*txState` — a struct containing an `atomic.Int32` for depth and a mutex-guarded slice for post-commit hooks.
- `BeginTransaction()` creates `*txState` on first call (outermost), increments depth, defers decrement, calls `db.Transaction()`, and fires hooks only when depth returns to 0 with no error.
- `OnCommit()` appends to the hook slice; hooks fire in registration order (FIFO).
- All public query functions (`IsOutermostTransaction`, `InTransaction`, `TransactionDepth`) are safe to call from any goroutine and from inside GORM callbacks.

## Files

| File | Contents |
|---|---|
| `txtracker.go` | Plugin struct, `BeginTransaction`, `OnCommit`, `IsOutermostTransaction`, `InTransaction`, `TransactionDepth`, `txState`, context key |
| `txtracker_test.go` | All tests (see IMPLEMENTATION.md for the full test plan) |
| `go.mod` | Module declaration, depends on `gorm.io/gorm` |
| `README.md` | User-facing installation, usage, API reference |
| `AGENTS.md` | This file |
| `IMPLEMENTATION.md` | Detailed implementation spec with code, flows, edge cases, testing guide |

## Things to never do in this plugin

- Do NOT use `Statement.Settings` / `db.Set()` / `db.Get()` for depth tracking — they are unreliable across transaction boundaries.
- Do NOT use `db.InstanceSet()` / `db.InstanceGet()` — these are scoped to a single `*Statement` pointer and invisible to other operations.
- Do NOT replace or remove any built-in GORM callback (`gorm:begin_transaction`, `gorm:commit_or_rollback_transaction`, etc.).
- Do NOT store mutable state on the `TxTracker` struct itself — all state must be in context.
- Do NOT call `context.Background()` — always use `db.Statement.Context`.
- Do NOT assume `db.Statement.Schema` is non-nil inside any helper that might be called from raw SQL paths.
