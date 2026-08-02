package store

import (
	"context"

	"gorm.io/gorm"
)

type txContextKey struct{}

// WithTransaction starts a DB transaction, stores it in ctx, and runs fn.
// Store methods that use DB(ctx, ...) or RunInTransaction join this TX.
func WithTransaction(ctx context.Context, db *gorm.DB, fn func(ctx context.Context) error) error {
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return fn(context.WithValue(ctx, txContextKey{}, tx))
	})
}

// DB returns the transaction from ctx when one is active; otherwise fallback.WithContext(ctx).
func DB(ctx context.Context, fallback *gorm.DB) *gorm.DB {
	if tx, ok := ctx.Value(txContextKey{}).(*gorm.DB); ok && tx != nil {
		return tx
	}
	if fallback == nil {
		return nil
	}
	return fallback.WithContext(ctx)
}

// InTransaction reports whether ctx carries an active store transaction.
func InTransaction(ctx context.Context) bool {
	tx, ok := ctx.Value(txContextKey{}).(*gorm.DB)
	return ok && tx != nil
}

// RunInTransaction runs fn on the active TX from ctx when present; otherwise
// starts a new transaction on db. This preserves caller-owned TX semantics
// (e.g. service delete + owner cleanup) without nesting a committing inner TX.
func RunInTransaction(ctx context.Context, db *gorm.DB, fn func(tx *gorm.DB) error) error {
	if InTransaction(ctx) {
		return fn(DB(ctx, db))
	}
	return db.WithContext(ctx).Transaction(fn)
}
