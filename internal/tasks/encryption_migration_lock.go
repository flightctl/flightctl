package tasks

import (
	"context"
	"fmt"
	"hash/fnv"
	"time"

	"gorm.io/gorm"
)

const encryptionMigrationUnlockTimeout = 5 * time.Second

// encryptionMigrationLockNamespace isolates advisory locks from other app uses.
const encryptionMigrationLockNamespace int32 = 0x45434d47 // ECMG

// EncryptionMigrationLocker serializes migration of one kind/org across worker replicas.
// Successful TryLock must be released with the returned unlock function.
// The key is typically leaseKey(kind, orgID).
type EncryptionMigrationLocker interface {
	TryLock(ctx context.Context, key string) (unlock func() error, acquired bool, err error)
}

type noopEncryptionMigrationLocker struct{}

func (noopEncryptionMigrationLocker) TryLock(context.Context, string) (func() error, bool, error) {
	return func() error { return nil }, true, nil
}

// PostgresEncryptionMigrationLocker uses session-scoped pg_try_advisory_lock.
type PostgresEncryptionMigrationLocker struct {
	db *gorm.DB
}

func NewPostgresEncryptionMigrationLocker(db *gorm.DB) *PostgresEncryptionMigrationLocker {
	return &PostgresEncryptionMigrationLocker{db: db}
}

func (l *PostgresEncryptionMigrationLocker) TryLock(ctx context.Context, key string) (func() error, bool, error) {
	if l == nil || l.db == nil {
		return nil, false, fmt.Errorf("encryption migration: postgres locker has nil db")
	}
	if key == "" {
		return nil, false, fmt.Errorf("encryption migration: lease key is required")
	}

	sqlDB, err := l.db.DB()
	if err != nil {
		return nil, false, fmt.Errorf("encryption migration: get sql db: %w", err)
	}
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("encryption migration: acquire db conn for lease: %w", err)
	}

	k1, k2 := encryptionMigrationAdvisoryKeys(key)
	var acquired bool
	if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1, $2)`, k1, k2).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, false, fmt.Errorf("encryption migration: try advisory lock for %s: %w", key, err)
	}
	if !acquired {
		_ = conn.Close()
		return nil, false, nil
	}

	unlock := func() error {
		defer func() { _ = conn.Close() }()
		unlockCtx, cancel := context.WithTimeout(context.Background(), encryptionMigrationUnlockTimeout)
		defer cancel()
		var unlocked bool
		if err := conn.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock($1, $2)`, k1, k2).Scan(&unlocked); err != nil {
			return fmt.Errorf("encryption migration: unlock %s: %w", key, err)
		}
		if !unlocked {
			return fmt.Errorf("encryption migration: unlock %s returned false", key)
		}
		return nil
	}
	return unlock, true, nil
}

func encryptionMigrationAdvisoryKeys(key string) (int32, int32) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return encryptionMigrationLockNamespace, int32(h.Sum32())
}
