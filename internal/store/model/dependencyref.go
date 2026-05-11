// Copyright (c) Flight Control Authors. Licensed under Apache-2.0.

package model

import "github.com/google/uuid"

// DependencyRef maps a fleet or device to an external dependency (git repo,
// HTTP resource, or K8s secret). The sync controller reads these rows as a
// polling work list (git/HTTP) and fan-out lookup (all types).
type DependencyRef struct {
	OrgID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	FleetName       *string   `gorm:"primaryKey;default:''"`
	DeviceName      *string   `gorm:"primaryKey;default:''"`
	RefType         string    `gorm:"primaryKey"` // "git", "http", "secret"
	RepositoryName  *string   `gorm:"primaryKey;default:''"`
	Revision        *string
	HTTPSuffix      *string
	SecretName      *string `gorm:"primaryKey;default:''"`
	SecretNamespace *string `gorm:"primaryKey;default:''"`
	SyncInterval    *string // Go duration string, e.g. "5m"
}

func (DependencyRef) TableName() string {
	return "dependency_refs"
}
