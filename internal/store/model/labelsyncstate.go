package model

import "github.com/google/uuid"

type LabelSyncState struct {
	OrgID        uuid.UUID `gorm:"type:uuid;primaryKey"`
	ResourceType string    `gorm:"primaryKey"`
	Revision     int64     `gorm:"not null"`
}

func (LabelSyncState) TableName() string { return "label_sync_state" }
