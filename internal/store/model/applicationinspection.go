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
	Values        InspectionFingerprint
	Dependencies  InspectionFingerprint
	RenderContext InspectionFingerprint
	ReleaseName   string
	Namespace     string
}

type ApplicationInspectionInput struct {
	Provider          InspectionProvider
	Format            InspectionFormat
	InlineFingerprint InspectionFingerprint
	ParentDigest      digest.Digest
	Platform          string
	Helm              *InspectionHelmIdentity
}

type InspectionState string

const (
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
	State           InspectionState            `gorm:"not null;check:application_inspection_state,state IN ('complete','finished_incomplete')"`
	Revision        InspectionFingerprint      `gorm:"type:jsonb;serializer:json;not null"`
	DiagnosticCodes []InspectionDiagnostic     `gorm:"type:jsonb;serializer:json"`
	CreatedAt       time.Time                  `gorm:"not null;index:inspection_cleanup_age,priority:2"`
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
	OrgID        uuid.UUID               `gorm:"type:uuid;primaryKey;index:inspection_ref_scope,priority:1"`
	InspectionID uuid.UUID               `gorm:"type:uuid;primaryKey"`
	Reference    string                  `gorm:"primaryKey"`
	Role         InspectionArtifactRole  `gorm:"primaryKey"`
	Mechanism    InspectionPullMechanism `gorm:"primaryKey"`
	Registry     string                  `gorm:"not null;index:inspection_ref_scope,priority:2"`
	Port         uint16                  `gorm:"not null"`
	Path         StringArray             `gorm:"type:text[];not null;index:inspection_ref_scope,priority:3"`
}

type ApplicationInspectionUsage struct {
	OrgID              uuid.UUID               `gorm:"type:uuid;primaryKey;uniqueIndex:inspection_usage_device,priority:1;uniqueIndex:inspection_usage_fleet,priority:1;index:inspection_usage_pending,priority:1;index:inspection_usage_result,priority:1"`
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
	ContinuationID     *uuid.UUID              `gorm:"type:uuid"`
}
