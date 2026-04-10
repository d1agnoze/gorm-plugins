// Package txtracker provides transaction depth tracking for GORM v2.
//
// GORM v2 has no built-in mechanism to track transaction nesting depth.
// This package provides a BeginTransaction wrapper that tracks depth via
// context.Context and supports post-commit hooks that fire only after the
// outermost transaction commits successfully.
package txtracker

import (
	"context"
	"database/sql"
	"sync"
	"sync/atomic"

	"gorm.io/gorm"
)

// txCtxKey is the unexported context key for storing *txState.
type txCtxKey struct{}

// txState holds the shared mutable state for a transaction tree.
//
// A single *txState is created at the outermost BeginTransaction call and
// shared through context by all nested BeginTransaction calls.
type txState struct {
	depth atomic.Int32

	mu    sync.Mutex
	hooks []func()
}

// TxTracker implements the gorm.Plugin interface.
//
// TxTracker registers no callbacks. Its functionality is exposed through the
// BeginTransaction wrapper and transaction state query helpers.
type TxTracker struct{}

// Name returns the plugin name used in db.Config.Plugins.
func (*TxTracker) Name() string {
	return "txtracker"
}

// Initialize is a no-op because TxTracker does not register GORM callbacks.
func (*TxTracker) Initialize(*gorm.DB) error {
	return nil
}

// BeginTransaction wraps db.Transaction with transaction depth tracking.
//
// On the outermost call it injects a shared *txState into the DB context.
// Nested calls reuse the same state and increment the shared depth counter.
// Hooks registered with OnCommit are executed synchronously in FIFO order only
// after the outermost underlying transaction commits successfully.
//
// If a hook panics, remaining hooks are not executed and the panic propagates.
func BeginTransaction(db *gorm.DB, fc func(tx *gorm.DB) error, opts ...*sql.TxOptions) error {
	ctx := db.Statement.Context
	state, _ := ctx.Value(txCtxKey{}).(*txState)
	isOutermost := state == nil

	if isOutermost {
		state = &txState{}
		ctx = context.WithValue(ctx, txCtxKey{}, state)
		db = db.WithContext(ctx)
	}

	state.depth.Add(1)
	err := db.Transaction(fc, opts...)
	newDepth := state.depth.Add(-1)

	if isOutermost {
		if err == nil && newDepth == 0 {
			state.mu.Lock()
			hooks := state.hooks
			state.hooks = nil
			state.mu.Unlock()

			for _, hook := range hooks {
				hook()
			}
		} else if newDepth == 0 {
			state.mu.Lock()
			state.hooks = nil
			state.mu.Unlock()
		}
	}

	return err
}

// OnCommit registers fn to run after the outermost transaction commits
// successfully.
//
// OnCommit must be called from inside a BeginTransaction context. If called
// without active txtracker state, it panics. If the outermost transaction rolls
// back, registered hooks are discarded silently.
func OnCommit(db *gorm.DB, fn func()) {
	state := transactionState(db)
	if state == nil {
		panic("txtracker.OnCommit called outside of BeginTransaction context")
	}

	state.mu.Lock()
	state.hooks = append(state.hooks, fn)
	state.mu.Unlock()
}

// IsOutermostTransaction reports whether the current context is at the
// outermost transaction level.
//
// If BeginTransaction was not used and no txtracker state exists in context, it
// returns true.
func IsOutermostTransaction(db *gorm.DB) bool {
	state := transactionState(db)
	if state == nil {
		return true
	}

	return state.depth.Load() == 1
}

// InTransaction reports whether the current context is inside a
// BeginTransaction call.
func InTransaction(db *gorm.DB) bool {
	state := transactionState(db)
	if state == nil {
		return false
	}

	return state.depth.Load() > 0
}

// TransactionDepth returns the current tracked transaction nesting depth.
func TransactionDepth(db *gorm.DB) int {
	state := transactionState(db)
	if state == nil {
		return 0
	}

	return int(state.depth.Load())
}

func transactionState(db *gorm.DB) *txState {
	if db == nil || db.Statement == nil || db.Statement.Context == nil {
		return nil
	}

	state, _ := db.Statement.Context.Value(txCtxKey{}).(*txState)
	return state
}
