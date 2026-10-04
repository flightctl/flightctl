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
	DeltaPrepareFailing  = "failing"
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
	ID                      uuid.UUID `gorm:"type:uuid;primaryKey"`
	OrgID                   uuid.UUID `gorm:"type:uuid;index"`
	Kind                    string    `gorm:"type:text"`
	Name                    string    `gorm:"type:text"`
	TemplateVersion         *string   `gorm:"type:text"`
	Generation              *int64
	DeviceCreationTimestamp *time.Time
	SourceResourceVersion   int64
	Deadline                *time.Time
	CreatedAt               time.Time
	PendingGenerationsCount int    `gorm:"not null;default:0"`
	Status                  string `gorm:"type:text"`
	ResourceVersion         int64
}

func (DeltaPrepare) TableName() string {
	return "delta_prepares"
}

// MatchesTarget compares the desired fleet template or standalone device generation.
// SourceResourceVersion is checked separately when ordering prepare events.
func (p *DeltaPrepare) MatchesTarget(templateVersion *string, generation *int64, deviceCreationTimestamp *time.Time) bool {
	return equalPointer(p.TemplateVersion, templateVersion) && equalPointer(p.Generation, generation) && equalTime(p.DeviceCreationTimestamp, deviceCreationTimestamp)
}

// CompareSource orders incarnations before resource versions, which restart on
// re-enrollment. Nil timestamps belong to fleet prepares or legacy device work.
func (p *DeltaPrepare) CompareSource(deviceCreationTimestamp *time.Time, resourceVersion int64) int {
	if !equalTime(p.DeviceCreationTimestamp, deviceCreationTimestamp) {
		if p.DeviceCreationTimestamp == nil {
			return -1
		}
		if deviceCreationTimestamp == nil {
			return 1
		}
		return p.DeviceCreationTimestamp.Compare(*deviceCreationTimestamp)
	}
	if p.SourceResourceVersion < resourceVersion {
		return -1
	}
	if p.SourceResourceVersion > resourceVersion {
		return 1
	}
	return 0
}

func equalTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func equalPointer[T comparable](a, b *T) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// DeltaPrepareGeneration is the join row that attaches a DeltaPrepare to the
// DeltaGeneration keys it is waiting on. Completed prevents a redelivered
// terminal-generation notification from decrementing the prepare again.
type DeltaPrepareGeneration struct {
	PrepareID       uuid.UUID `gorm:"type:uuid;primaryKey"`
	OrgID           uuid.UUID `gorm:"type:uuid;primaryKey"`
	ImageRepository string    `gorm:"type:text;primaryKey"`
	SourceDigest    string    `gorm:"type:text;primaryKey"`
	TargetDigest    string    `gorm:"type:text;primaryKey"`
	Completed       bool      `gorm:"not null;default:false"`
}

func (DeltaPrepareGeneration) TableName() string {
	return "delta_prepare_generations"
}
