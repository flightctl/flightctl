package model

import "time"

// SchemaMigration tracks one-time data-migration steps that must run exactly once
// across all replicas. The Key is the primary key; inserting with ON CONFLICT DO NOTHING
// acts as a distributed lock: whichever replica wins the insert runs the migration,
// while concurrent replicas see 0 rows affected and skip it.
type SchemaMigration struct {
	Key       string    `gorm:"primaryKey"`
	AppliedAt time.Time `gorm:"not null"`
}
