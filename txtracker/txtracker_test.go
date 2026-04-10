package txtracker_test

import (
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/d1agnoze/gorm-plugins/txtracker"
)

type testUser struct {
	gorm.Model
	Name string
}

func setupTestDB(t *testing.T, opts ...func(*gorm.Config)) *gorm.DB {
	t.Helper()

	config := &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)}
	for _, opt := range opts {
		opt(config)
	}

	dsn := fmt.Sprintf(
		"file:%s?mode=memory&cache=shared",
		strings.NewReplacer("/", "_", " ", "_").Replace(t.Name()),
	)

	db, err := gorm.Open(sqlite.Open(dsn), config)
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}

	if err := db.AutoMigrate(&testUser{}); err != nil {
		t.Fatalf("failed to migrate: %v", err)
	}

	if err := db.Use(&txtracker.TxTracker{}); err != nil {
		t.Fatalf("failed to register plugin: %v", err)
	}

	return db
}

func TestPluginRegistration(t *testing.T) {
	db := setupTestDB(t)

	if _, ok := db.Config.Plugins["txtracker"]; !ok {
		t.Fatal("plugin not found in config")
	}

	var plugin txtracker.TxTracker
	if plugin.Name() != "txtracker" {
		t.Fatalf("unexpected plugin name: %q", plugin.Name())
	}
}

func TestDepthWithoutBeginTransaction(t *testing.T) {
	db := setupTestDB(t)

	type observedState struct {
		isOutermost bool
		inTx        bool
		depth       int
	}

	var observed observedState
	if err := db.Callback().Create().After("gorm:commit_or_rollback_transaction").
		Register("txtracker_test:capture_default_state", func(tx *gorm.DB) {
			observed = observedState{
				isOutermost: txtracker.IsOutermostTransaction(tx),
				inTx:        txtracker.InTransaction(tx),
				depth:       txtracker.TransactionDepth(tx),
			}
		}); err != nil {
		t.Fatalf("failed to register callback: %v", err)
	}

	if err := db.Create(&testUser{Name: "alice"}).Error; err != nil {
		t.Fatalf("create failed: %v", err)
	}

	if !observed.isOutermost {
		t.Fatal("expected outermost transaction to be true")
	}

	if observed.inTx {
		t.Fatal("expected InTransaction to be false")
	}

	if observed.depth != 0 {
		t.Fatalf("expected depth 0, got %d", observed.depth)
	}
}

func TestSingleTransaction(t *testing.T) {
	db := setupTestDB(t)

	err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		if err := tx.Create(&testUser{Name: "alice"}).Error; err != nil {
			return err
		}

		if depth := txtracker.TransactionDepth(tx); depth != 1 {
			return fmt.Errorf("expected depth 1, got %d", depth)
		}

		if !txtracker.IsOutermostTransaction(tx) {
			return errors.New("expected outermost transaction")
		}

		if !txtracker.InTransaction(tx) {
			return errors.New("expected to be in transaction")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	var user testUser
	if err := db.Where("name = ?", "alice").First(&user).Error; err != nil {
		t.Fatalf("expected user to persist: %v", err)
	}
}

func TestNestedTransactionDepth(t *testing.T) {
	db := setupTestDB(t)

	err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		if depth := txtracker.TransactionDepth(tx); depth != 1 {
			return fmt.Errorf("expected outer depth 1, got %d", depth)
		}

		if !txtracker.IsOutermostTransaction(tx) {
			return errors.New("expected outer transaction to be outermost")
		}

		if err := txtracker.BeginTransaction(tx, func(tx2 *gorm.DB) error {
			if depth := txtracker.TransactionDepth(tx2); depth != 2 {
				return fmt.Errorf("expected nested depth 2, got %d", depth)
			}

			if txtracker.IsOutermostTransaction(tx2) {
				return errors.New("expected nested transaction not to be outermost")
			}

			if !txtracker.InTransaction(tx2) {
				return errors.New("expected nested transaction to be active")
			}

			return txtracker.BeginTransaction(tx2, func(tx3 *gorm.DB) error {
				if depth := txtracker.TransactionDepth(tx3); depth != 3 {
					return fmt.Errorf("expected nested depth 3, got %d", depth)
				}

				if txtracker.IsOutermostTransaction(tx3) {
					return errors.New("expected deepest transaction not to be outermost")
				}

				return nil
			})
		}); err != nil {
			return err
		}

		if depth := txtracker.TransactionDepth(tx); depth != 1 {
			return fmt.Errorf("expected depth to return to 1, got %d", depth)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}
}

func TestOnCommitFiresAfterOutermostCommit(t *testing.T) {
	db := setupTestDB(t)
	hookCalled := false

	err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		if err := tx.Create(&testUser{Name: "alice"}).Error; err != nil {
			return err
		}

		txtracker.OnCommit(tx, func() { hookCalled = true })

		if hookCalled {
			return errors.New("hook ran before transaction committed")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	if !hookCalled {
		t.Fatal("expected hook to run after commit")
	}
}

func TestOnCommitNotCalledOnRollback(t *testing.T) {
	db := setupTestDB(t)
	hookCalled := false

	err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		if err := tx.Create(&testUser{Name: "alice"}).Error; err != nil {
			return err
		}

		txtracker.OnCommit(tx, func() { hookCalled = true })
		return errors.New("rollback")
	})
	if err == nil {
		t.Fatal("expected rollback error")
	}

	if hookCalled {
		t.Fatal("expected hook not to run")
	}

	var user testUser
	err = db.Where("name = ?", "alice").First(&user).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected record not found after rollback, got %v", err)
	}
}

func TestNestedOnCommitDefersToOutermost(t *testing.T) {
	db := setupTestDB(t)
	var order []string

	err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		txtracker.OnCommit(tx, func() { order = append(order, "outer") })

		if err := txtracker.BeginTransaction(tx, func(tx2 *gorm.DB) error {
			txtracker.OnCommit(tx2, func() { order = append(order, "inner") })
			return nil
		}); err != nil {
			return err
		}

		if len(order) != 0 {
			return fmt.Errorf("expected no hooks to run yet, got %v", order)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	if !reflect.DeepEqual(order, []string{"outer", "inner"}) {
		t.Fatalf("unexpected hook order: %v", order)
	}
}

func TestNestedInnerRollbackOuterCommit(t *testing.T) {
	db := setupTestDB(t)
	var order []string

	err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		if err := tx.Create(&testUser{Name: "alice"}).Error; err != nil {
			return err
		}

		txtracker.OnCommit(tx, func() { order = append(order, "outer") })

		innerErr := txtracker.BeginTransaction(tx, func(tx2 *gorm.DB) error {
			if err := tx2.Create(&testUser{Name: "bob"}).Error; err != nil {
				return err
			}

			txtracker.OnCommit(tx2, func() { order = append(order, "inner") })
			return errors.New("inner failed")
		})
		if innerErr == nil {
			return errors.New("expected inner transaction error")
		}

		return nil
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	if !reflect.DeepEqual(order, []string{"outer", "inner"}) {
		t.Fatalf("unexpected hook order: %v", order)
	}

	var alice testUser
	if err := db.Where("name = ?", "alice").First(&alice).Error; err != nil {
		t.Fatalf("expected alice to persist: %v", err)
	}

	var bob testUser
	err = db.Where("name = ?", "bob").First(&bob).Error
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("expected bob to be rolled back, got %v", err)
	}
}

func TestOnCommitPanicOutsideTransaction(t *testing.T) {
	db := setupTestDB(t)

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}

		if msg := fmt.Sprint(r); msg != "txtracker.OnCommit called outside of BeginTransaction context" {
			t.Fatalf("unexpected panic message: %q", msg)
		}
	}()

	txtracker.OnCommit(db, func() {})
}

func TestDepthVisibleInCallbacks(t *testing.T) {
	db := setupTestDB(t)

	type capturedState struct {
		depth        int
		isOutermost  bool
		observedName string
	}

	var captured []capturedState
	if err := db.Callback().Create().After("gorm:commit_or_rollback_transaction").
		Register("txtracker_test:capture_depth", func(tx *gorm.DB) {
			var user testUser
			_ = tx.Statement.Dest
			if dest, ok := tx.Statement.Dest.(*testUser); ok && dest != nil {
				user = *dest
			}

			captured = append(captured, capturedState{
				depth:        txtracker.TransactionDepth(tx),
				isOutermost:  txtracker.IsOutermostTransaction(tx),
				observedName: user.Name,
			})
		}); err != nil {
		t.Fatalf("failed to register callback: %v", err)
	}

	if err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		return tx.Create(&testUser{Name: "alice"}).Error
	}); err != nil {
		t.Fatalf("outer transaction failed: %v", err)
	}

	if len(captured) != 1 {
		t.Fatalf("expected 1 callback capture, got %d", len(captured))
	}

	if captured[0].depth != 1 || !captured[0].isOutermost {
		t.Fatalf("unexpected first capture: %+v", captured[0])
	}

	if err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		return txtracker.BeginTransaction(tx, func(tx2 *gorm.DB) error {
			return tx2.Create(&testUser{Name: "bob"}).Error
		})
	}); err != nil {
		t.Fatalf("nested transaction failed: %v", err)
	}

	if len(captured) != 2 {
		t.Fatalf("expected 2 callback captures, got %d", len(captured))
	}

	if captured[1].depth != 2 || captured[1].isOutermost {
		t.Fatalf("unexpected second capture: %+v", captured[1])
	}
}

func TestSkipDefaultTransaction(t *testing.T) {
	db := setupTestDB(t, func(config *gorm.Config) {
		config.SkipDefaultTransaction = true
	})
	hookCalled := false

	err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		if err := tx.Create(&testUser{Name: "alice"}).Error; err != nil {
			return err
		}

		txtracker.OnCommit(tx, func() { hookCalled = true })

		if depth := txtracker.TransactionDepth(tx); depth != 1 {
			return fmt.Errorf("expected depth 1, got %d", depth)
		}

		return nil
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	if !hookCalled {
		t.Fatal("expected hook to run")
	}

	var user testUser
	if err := db.Where("name = ?", "alice").First(&user).Error; err != nil {
		t.Fatalf("expected user to persist: %v", err)
	}
}

func TestConcurrentTransactions(t *testing.T) {
	db := setupTestDB(t)

	var wg sync.WaitGroup
	results := make([]int, 2)
	errs := make([]error, 2)

	wg.Add(2)

	go func() {
		defer wg.Done()
		errs[0] = txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
			results[0] = txtracker.TransactionDepth(tx)
			return tx.Create(&testUser{Name: "goroutine1"}).Error
		})
	}()

	go func() {
		defer wg.Done()
		errs[1] = txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
			return txtracker.BeginTransaction(tx, func(tx2 *gorm.DB) error {
				results[1] = txtracker.TransactionDepth(tx2)
				return tx2.Create(&testUser{Name: "goroutine2"}).Error
			})
		})
	}()

	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("transaction %d failed: %v", i, err)
		}
	}

	if results[0] != 1 {
		t.Fatalf("expected goroutine 1 depth 1, got %d", results[0])
	}

	if results[1] != 2 {
		t.Fatalf("expected goroutine 2 depth 2, got %d", results[1])
	}
}

func TestOnCommitExecutionOrder(t *testing.T) {
	db := setupTestDB(t)
	var order []int

	err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		txtracker.OnCommit(tx, func() { order = append(order, 1) })
		txtracker.OnCommit(tx, func() { order = append(order, 2) })
		txtracker.OnCommit(tx, func() { order = append(order, 3) })
		return nil
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	if !slices.Equal(order, []int{1, 2, 3}) {
		t.Fatalf("unexpected hook order: %v", order)
	}
}

func TestDepthReturnToZeroAfterCompletion(t *testing.T) {
	db := setupTestDB(t)
	var depthInside int

	err := txtracker.BeginTransaction(db, func(tx *gorm.DB) error {
		depthInside = txtracker.TransactionDepth(tx)
		return nil
	})
	if err != nil {
		t.Fatalf("BeginTransaction failed: %v", err)
	}

	if depthInside != 1 {
		t.Fatalf("expected inside depth 1, got %d", depthInside)
	}

	if depth := txtracker.TransactionDepth(db); depth != 0 {
		t.Fatalf("expected original db depth 0, got %d", depth)
	}
}
