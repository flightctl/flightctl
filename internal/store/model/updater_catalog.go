package model

import "github.com/google/uuid"

// UpdaterCatalogHead serializes installation-wide administrator policy activation.
type UpdaterCatalogHead struct {
	ID           int    `gorm:"primaryKey;check:updater_catalog_singleton,id = 1"`
	Revision     int64  `gorm:"not null;default:0"`
	InputDigest  string `gorm:"not null"`
	PolicyDigest string `gorm:"not null"`
}

func (UpdaterCatalogHead) TableName() string { return "updater_catalog_heads" }

type UpdaterRelease struct {
	Name             string `gorm:"primaryKey;size:128"`
	Version          string `gorm:"primaryKey;size:128"`
	DescriptorDigest string `gorm:"not null;size:71"`
	Descriptor       []byte `gorm:"not null;type:bytea"`
}

func (UpdaterRelease) TableName() string { return "updater_releases" }

type UpdaterRevocation struct {
	Name             string `gorm:"primaryKey;size:128"`
	Version          string `gorm:"primaryKey;size:128"`
	DescriptorDigest string `gorm:"not null;size:71"`
	Reason           string `gorm:"not null;size:1024"`
	Revision         int64  `gorm:"not null"`
}

func (UpdaterRevocation) TableName() string { return "updater_revocations" }

type UpdaterSchemaEvidence struct {
	Name              string   `gorm:"primaryKey;size:128"`
	Digest            string   `gorm:"primaryKey;size:71"`
	ObservationSchema []byte   `gorm:"not null;type:bytea"`
	BudgetKeys        []string `gorm:"not null;type:jsonb;serializer:json"`
}

func (UpdaterSchemaEvidence) TableName() string { return "updater_schema_evidence" }

// UpdaterCatalogRefresh remains durable until recipient fanout has completed.
type UpdaterCatalogRefresh struct {
	Revision    int64     `gorm:"primaryKey;autoIncrement:false"`
	CursorOrgID uuid.UUID `gorm:"type:uuid;not null;default:'00000000-0000-0000-0000-000000000000'"`
	CursorName  string    `gorm:"not null;default:''"`
	Dispatched  bool      `gorm:"not null;default:false;index"`
}

func (UpdaterCatalogRefresh) TableName() string { return "updater_catalog_refreshes" }

// UpdaterPolicyTarget keeps one monotonic delivery high-water per device.
// NotifiedRevision records publication, not durable agent receipt.
type UpdaterPolicyTarget struct {
	OrgID            uuid.UUID `gorm:"primaryKey;type:uuid"`
	DeviceName       string    `gorm:"primaryKey"`
	Revision         int64     `gorm:"not null;check:updater_policy_target_positive,revision > 0"`
	ReceivedRevision int64     `gorm:"not null;default:0;check:updater_policy_receipt_bound,received_revision >= 0 AND received_revision <= revision"`
	ReceivedDigest   string    `gorm:"not null;default:''"`
	NotifiedRevision int64     `gorm:"not null;default:0;check:updater_policy_notification_bound,notified_revision >= 0 AND notified_revision <= revision"`
}

func (UpdaterPolicyTarget) TableName() string { return "updater_policy_targets" }
