package model

import (
	"time"

	"github.com/google/uuid"
)

// EnrollmentHookNotifySecret stores encrypted bearer tokens for enrollment
// hook notify actions. Keyed by (org_id, device_name, action_index).
type EnrollmentHookNotifySecret struct {
	OrgID       uuid.UUID `gorm:"type:uuid;primaryKey"`
	DeviceName  string    `gorm:"primaryKey"`
	ActionIndex int       `gorm:"primaryKey"`
	BearerToken string    `gorm:"type:text"`
	CreatedAt   time.Time
}

func (EnrollmentHookNotifySecret) TableName() string {
	return "enrollment_hook_notify_secrets"
}
