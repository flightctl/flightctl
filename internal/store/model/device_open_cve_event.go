package model

import (
	"time"

	"github.com/google/uuid"
)

// DeviceOpenCveEvent tracks currently open CVE events per device.
// This table serves as both a state cache for efficient delta computation
// and a synchronization primitive to prevent duplicate event emission.
// Primary key is (org_id, device_name, cve_id).
type DeviceOpenCveEvent struct {
	OrgID            uuid.UUID `gorm:"type:uuid;primaryKey"`
	DeviceName       string    `gorm:"type:text;primaryKey"`
	CveID            string    `gorm:"type:text;primaryKey;column:cve_id"`
	Severity         string    `gorm:"type:text;not null"` // Critical or High
	FirstImageDigest *string   `gorm:"type:text"`
	FirstImageRef    *string   `gorm:"type:text"`
	CreatedAt        time.Time `gorm:"type:timestamptz;not null;default:now()"`
}

func (DeviceOpenCveEvent) TableName() string {
	return "device_open_cve_events"
}

// ChangedFinding represents a vulnerability finding that was inserted or updated
// during the Trustify sync. Used to compute CVE event actions.
type ChangedFinding struct {
	ImageDigest string
	CveID       string
	Severity    string // Current: Critical, High, Medium, Low, None, Unknown
	Status      string // Current: affected, not_affected, fixed, unknown
}

// CVEEventAction represents an action to be performed on device_open_cve_events
// and the corresponding event to emit.
type CVEEventAction struct {
	OrgID            uuid.UUID
	DeviceName       string
	CveID            string
	Action           string // "new", "supersede", "resolved"
	Severity         string
	FirstImageDigest *string
	FirstImageRef    *string
}

// CVEEventActionType constants for action field.
const (
	CVEEventActionNew       = "new"
	CVEEventActionSupersede = "supersede"
	CVEEventActionResolved  = "resolved"
)
