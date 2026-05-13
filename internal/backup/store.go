package backup

import (
	"gorm.io/gorm"
)

// BackupStore provides database operations for backup functionality.
// This is a minimal placeholder parallel to restore.RestoreStore.
type BackupStore struct {
	db *gorm.DB
}

// NewBackupStore creates a BackupStore backed by the given gorm connection.
func NewBackupStore(db *gorm.DB) *BackupStore {
	return &BackupStore{db: db}
}

// Placeholder methods will be added in future stories as backup logic is implemented
