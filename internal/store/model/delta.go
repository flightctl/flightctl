package model

import (
	"time"

	"github.com/google/uuid"
)

const (
	DeltaGenerationPending    = "pending"
	DeltaGenerationInProgress = "in_progress"
	DeltaGenerationSucceeded  = "succeeded"
	DeltaGenerationFailed     = "failed"
	DeltaGenerationRejected   = "rejected"

	DeltaPrepareWaiting  = "waiting"
	DeltaPrepareComplete = "complete"
	DeltaPrepareFailed   = "failed"
)

type DeltaGeneration struct {
	OrgID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	ImageRepository string    `gorm:"type:text;primaryKey"`
	SourceDigest    string    `gorm:"type:text;primaryKey"`
	TargetDigest    string    `gorm:"type:text;primaryKey"`

	DeltaRef        *string
	SizeBytes       *int64
	Status          string `gorm:"type:text"`
	LastVerifiedAt  *time.Time
	GeneratedAt     *time.Time
	ResourceVersion int64
	Phase           *string
	UpdatedAt       time.Time
}

func (DeltaGeneration) TableName() string {
	return "delta_generations"
}

type DeltaPrepare struct {
	ID                  uuid.UUID `gorm:"type:uuid;primaryKey"`
	OrgID               uuid.UUID `gorm:"type:uuid;index"`
	Kind                string    `gorm:"type:text"`
	Name                string    `gorm:"type:text"`
	TemplateVersion     *string   `gorm:"type:text"`
	SpecResourceVersion *int64
	Deadline            *time.Time
	CreatedAt           time.Time
	Status              string `gorm:"type:text"`
}

func (DeltaPrepare) TableName() string {
	return "delta_prepares"
}

// DeltaPrepareGeneration is the join row that attaches a DeltaPrepare to the
// DeltaGeneration keys it is waiting on.
type DeltaPrepareGeneration struct {
	PrepareID       uuid.UUID `gorm:"type:uuid;primaryKey"`
	OrgID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	ImageRepository string    `gorm:"type:text;primaryKey"`
	SourceDigest    string    `gorm:"type:text;primaryKey"`
	TargetDigest    string    `gorm:"type:text;primaryKey"`
}

func (DeltaPrepareGeneration) TableName() string {
	return "delta_prepare_generations"
}
