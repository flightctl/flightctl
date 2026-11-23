package model

import (
	"crypto/sha256"
	"time"

	"github.com/google/uuid"
	"github.com/opencontainers/go-digest"
)

type InspectionProvider string

const (
	InspectionProviderCompose InspectionProvider = "compose"
	InspectionProviderQuadlet InspectionProvider = "quadlet"
	InspectionProviderHelm    InspectionProvider = "helm"
)

type InspectionFormat string

const (
	InspectionFormatInline InspectionFormat = "inline"
	InspectionFormatRaw    InspectionFormat = "raw"
	InspectionFormatTar    InspectionFormat = "tar"
	InspectionFormatTarGz  InspectionFormat = "tar.gz"
	InspectionFormatChart  InspectionFormat = "chart"
)

// InspectionFingerprint identifies non-secret inputs or resolved resource revisions.
type InspectionFingerprint [sha256.Size]byte

type InspectionHelmIdentity struct {
	Values         InspectionFingerprint
	Dependencies   InspectionFingerprint
	RenderContext  InspectionFingerprint
	ReleaseName    string
	Namespace      string
	ChartReference string        `json:",omitempty"`
	FreshFor       time.Duration `json:",omitempty"`
}

type ApplicationInspectionInput struct {
	Provider          InspectionProvider
	Format            InspectionFormat
	InlineFingerprint InspectionFingerprint
	ParentDigest      digest.Digest
	// Platform is set when selecting bundle content from an OCI index.
	Platform string
	Helm     *InspectionHelmIdentity
	Declared InspectionFingerprint `json:",omitzero"`
}

type InspectionState string

const (
	InspectionResolving          InspectionState = "resolving"
	InspectionPending            InspectionState = "pending"
	InspectionComplete           InspectionState = "complete"
	InspectionFinishedIncomplete InspectionState = "finished_incomplete"
)

type InspectionDiagnostic string

const (
	InspectionContentUnavailable InspectionDiagnostic = "content_unavailable"
	InspectionParseFailed        InspectionDiagnostic = "parse_failed"
	InspectionBudgetExceeded     InspectionDiagnostic = "budget_exceeded"
)

type ApplicationInspection struct {
	OrgID           uuid.UUID                  `gorm:"type:uuid;primaryKey;index:inspection_cleanup_age,priority:1"`
	ID              uuid.UUID                  `gorm:"type:uuid;primaryKey;index:inspection_cleanup_age,priority:3"`
	InputKey        string                     `gorm:"not null"`
	Input           ApplicationInspectionInput `gorm:"type:jsonb;serializer:json;not null"`
	State           InspectionState            `gorm:"not null;check:application_inspection_work_state,state IN ('resolving','pending','complete','finished_incomplete')"`
	Revision        InspectionFingerprint      `gorm:"type:jsonb;serializer:json;not null"`
	DiagnosticCodes []InspectionDiagnostic     `gorm:"type:jsonb;serializer:json"`
	ClaimToken      *uuid.UUID                 `gorm:"type:uuid"`
	ClaimUntil      *time.Time
	FreshUntil      *time.Time
	CreatedAt       time.Time `gorm:"not null;index:inspection_cleanup_age,priority:2"`
}

type InspectionArtifactRole string

const (
	InspectionRolePackage  InspectionArtifactRole = "package"
	InspectionRoleWorkload InspectionArtifactRole = "workload"
	InspectionRoleVolume   InspectionArtifactRole = "volume"
	InspectionRoleOS       InspectionArtifactRole = "os"
	InspectionRoleDelta    InspectionArtifactRole = "delta"
)

type InspectionPullMechanism string

const (
	InspectionPullContainer InspectionPullMechanism = "container"
	InspectionPullHelmChart InspectionPullMechanism = "helm-chart"
	InspectionPullHelmImage InspectionPullMechanism = "helm-image"
	InspectionPullOS        InspectionPullMechanism = "os"
)

type ApplicationInspectionRef struct {
	OrgID        uuid.UUID               `gorm:"type:uuid;primaryKey;index:inspection_ref_scope,priority:1" json:"org_id"`
	InspectionID uuid.UUID               `gorm:"type:uuid;primaryKey" json:"inspection_id"`
	Reference    string                  `gorm:"primaryKey" json:"reference"`
	Role         InspectionArtifactRole  `gorm:"primaryKey" json:"role"`
	Mechanism    InspectionPullMechanism `gorm:"primaryKey" json:"mechanism"`
	Registry     string                  `gorm:"not null;index:inspection_ref_scope,priority:2" json:"registry"`
	Port         uint16                  `gorm:"not null" json:"port"`
	Path         StringArray             `gorm:"type:text[];not null;index:inspection_ref_scope,priority:3" json:"path"`
}

type ApplicationInspectionUsage struct {
	OrgID              uuid.UUID               `gorm:"type:uuid;primaryKey;uniqueIndex:inspection_usage_device,priority:1;uniqueIndex:inspection_usage_fleet,priority:1;index:inspection_usage_pending,priority:1;index:inspection_usage_result,priority:1;index:inspection_usage_continuation,priority:1"`
	ID                 uuid.UUID               `gorm:"type:uuid;primaryKey"`
	InspectionID       uuid.UUID               `gorm:"type:uuid;not null;index:inspection_usage_result,priority:2"`
	DeviceName         *string                 `gorm:"uniqueIndex:inspection_usage_device,priority:2,where:device_name IS NOT NULL;check:inspection_usage_one_owner,(device_name IS NULL) <> (fleet_name IS NULL)"`
	FleetName          *string                 `gorm:"uniqueIndex:inspection_usage_fleet,priority:2,where:fleet_name IS NOT NULL"`
	Generation         int64                   `gorm:"not null"`
	OwnerCreatedAt     time.Time               `gorm:"not null"`
	TemplateIdentity   string                  `gorm:"not null"`
	ApplicationContext string                  `gorm:"not null;uniqueIndex:inspection_usage_device,priority:3;uniqueIndex:inspection_usage_fleet,priority:3"`
	User               string                  `gorm:"not null"`
	Mechanism          InspectionPullMechanism `gorm:"not null"`
	Pending            bool                    `gorm:"not null;index:inspection_usage_pending,priority:2"`
	ContinuationID     *uuid.UUID              `gorm:"type:uuid;index:inspection_usage_continuation,priority:2"`
}

type ApplicationInspectionContinuation struct {
	OrgID            uuid.UUID `gorm:"type:uuid;primaryKey"`
	ID               uuid.UUID `gorm:"type:uuid;primaryKey"`
	DeviceName       *string   `gorm:"check:inspection_continuation_one_owner,(device_name IS NULL) <> (fleet_name IS NULL)"`
	FleetName        *string
	Generation       int64                 `gorm:"not null"`
	OwnerCreatedAt   time.Time             `gorm:"not null"`
	TemplateIdentity string                `gorm:"not null"`
	Snapshot         InspectionFingerprint `gorm:"type:jsonb;serializer:json;not null"`
	EventID          uuid.UUID             `gorm:"type:uuid;not null"`
	Current          bool                  `gorm:"not null"`
	ConsumedAt       *time.Time
	SupersededAt     *time.Time
}
