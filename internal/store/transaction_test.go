package store

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	require.NoError(t, err)
	return db
}

func TestWithTransactionPutsTXInContext(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()

	err := WithTransaction(ctx, db, func(ctx context.Context) error {
		require.True(t, InTransaction(ctx))
		tx := DB(ctx, db)
		require.NotNil(t, tx)
		require.True(t, tx != db)
		return nil
	})
	require.NoError(t, err)
	require.False(t, InTransaction(ctx))
}

func TestRunInTransactionJoinsCallerTX(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	var seen *gorm.DB

	err := WithTransaction(ctx, db, func(ctx context.Context) error {
		outer := DB(ctx, db)
		return RunInTransaction(ctx, db, func(tx *gorm.DB) error {
			seen = tx
			require.Equal(t, outer, tx)
			return nil
		})
	})
	require.NoError(t, err)
	require.NotNil(t, seen)
}

func TestRunInTransactionStartsOwnTXWhenNoneActive(t *testing.T) {
	db := openTestDB(t)
	ctx := context.Background()
	require.False(t, InTransaction(ctx))

	err := RunInTransaction(ctx, db, func(tx *gorm.DB) error {
		require.NotNil(t, tx)
		return nil
	})
	require.NoError(t, err)
}

func TestWithTransactionRollsBackOnError(t *testing.T) {
	db := openTestDB(t)
	type row struct {
		ID int `gorm:"primaryKey"`
	}
	require.NoError(t, db.AutoMigrate(&row{}))

	boom := errors.New("boom")
	err := WithTransaction(context.Background(), db, func(ctx context.Context) error {
		require.NoError(t, DB(ctx, db).Create(&row{ID: 1}).Error)
		return boom
	})
	require.ErrorIs(t, err, boom)

	var count int64
	require.NoError(t, db.Model(&row{}).Count(&count).Error)
	require.Equal(t, int64(0), count)
}
