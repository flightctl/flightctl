// Copyright (c) Flight Control Authors. Licensed under Apache-2.0.

package model

import (
	"time"

	"github.com/google/uuid"
)

// SyncState tracks the last-known fingerprint of an external dependency
// (git ref, HTTP resource, or K8s secret) for change detection.
type SyncState struct {
	OrgID uuid.UUID `gorm:"type:uuid;primaryKey"`
	// Format: "git:<repo-name>/<ref>", "http:<repo-name>/<suffix>", "secret:<namespace>/<name>"
	ResourceKey   string `gorm:"primaryKey"`
	Fingerprint   string
	LastCheckedAt time.Time
	LastChangeAt  *time.Time
}

func (SyncState) TableName() string {
	return "sync_states"
}
