package model

import (
	"time"

	"github.com/google/uuid"
)

// DecommissionedDevice represents a device that has been decommissioned
type DecommissionedDevice struct {
	// Primary key - device name
	ID string `gorm:"primaryKey"`

	// Organization ID
	OrgID uuid.UUID `gorm:"type:uuid;index"`

	// Certificate expiration date
	CertificateExpirationDate time.Time `gorm:"index"`

	// Timestamp when the device was decommissioned
	DecommissionedAt time.Time `gorm:"index"`
}
