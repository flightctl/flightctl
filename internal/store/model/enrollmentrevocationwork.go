package model

import (
	"time"

	"github.com/google/uuid"
)

// EnrollmentRevocationWork is the durable outbox record for the consequence
// work of a committed enrollment credential revocation. It is written in the
// same transaction as the revocation and is reserved for the enrollment
// revocation consequence worker.
type EnrollmentRevocationWork struct {
	OrgID        uuid.UUID `gorm:"primaryKey;type:uuid"`
	CSRName      string    `gorm:"primaryKey"`
	CSRCreatedAt time.Time `gorm:"primaryKey"`
	CreatedAt    time.Time
}

// TableName keeps the outbox table name stable across model refactors.
func (EnrollmentRevocationWork) TableName() string {
	return "enrollment_revocation_work"
}
