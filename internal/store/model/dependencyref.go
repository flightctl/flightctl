package model

import (
	"database/sql/driver"
	"fmt"
	"strings"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
)

// StringArray is a []string that scans PostgreSQL text arrays ({a,b,c}).
type StringArray []string

func (a *StringArray) Scan(src interface{}) error {
	if src == nil {
		*a = nil
		return nil
	}
	var s string
	switch v := src.(type) {
	case string:
		s = v
	case []byte:
		s = string(v)
	default:
		return fmt.Errorf("StringArray.Scan: unsupported type %T", src)
	}
	s = strings.TrimPrefix(s, "{")
	s = strings.TrimSuffix(s, "}")
	if s == "" {
		*a = nil
		return nil
	}
	*a = strings.Split(s, ",")
	return nil
}

func (a StringArray) Value() (driver.Value, error) {
	if a == nil {
		return nil, nil
	}
	return "{" + strings.Join(a, ",") + "}", nil
}

func (StringArray) GormDataType() string {
	return "text[]"
}

// DependencyRef maps a fleet or device to an external dependency (git repo,
// HTTP resource, or K8s secret). The sync controller reads these rows as a
// polling work list (git/HTTP) and fan-out lookup (all types).
type DependencyRef struct {
	OrgID       uuid.UUID `gorm:"type:uuid;primaryKey"`
	ResourceKey string    `gorm:"primaryKey"` // e.g. "git:repo/ref", "http:repo/path", "secret:ns/name"
	FleetName   *string   `gorm:"primaryKey;default:''"`
	DeviceName  *string   `gorm:"primaryKey;default:''"`

	RefType            string // "git", "http", "secret"
	RepositoryName     *string
	Revision           *string
	HTTPSuffix         *string
	SecretName         *string
	SecretNamespace    *string
	ConfigProviderName string
}

func (DependencyRef) TableName() string {
	return "dependency_refs"
}

// SecretDependencyRef is a flat row returned by ListSecretDependencyTargets.
// One row per (org, fleet/device) referencing the queried secret. The query is
// cross-org because the K8s informer has no org context — it only knows
// (namespace, name) from the watch event.
type SecretDependencyRef struct {
	OrgID       uuid.UUID
	FleetName   string
	DeviceName  string
	Fingerprint *string
}

// DependencyRefWithSyncState joins a dependency ref with its sync state for
// status computation. Returned by ListDependencyRefsWithSyncState.
type DependencyRefWithSyncState struct {
	ResourceKey        string
	RefType            string
	ConfigProviderName string
	Fingerprint        *string
	ProbeStatus        *string
	ProbeMessage       *string
	LastCheckedAt      *time.Time
	LastChangeAt       *time.Time
}

// DependencyRefOwner is a distinct (fleet_name, device_name) pair returned by
// ListDistinctRefOwners to enumerate all entities that have dependency refs.
type DependencyRefOwner struct {
	FleetName  string
	DeviceName string
}

// GitDependencyProbe is the result of ListDueGitDependencies — one row per
// unique (repository_name, revision) pair that is due for polling. FleetNames
// and DeviceNames carry the fan-out targets collected via array_agg.
// RepoSpec carries the repository's JSONB spec so callers can extract the
// URL and auth without a separate GetRepository round-trip.
type GitDependencyProbe struct {
	RepositoryName string
	Revision       string
	Fingerprint    *string
	FleetNames     StringArray
	DeviceNames    StringArray
	RepoSpec       *JSONField[domain.RepositorySpec] `gorm:"type:jsonb"`
}

// HttpDependencyProbe is the result of ListDueHttpDependencies — one row per
// unique (repository_name, http_suffix) pair that is due for polling.
type HttpDependencyProbe struct {
	RepositoryName string
	HTTPSuffix     string
	Fingerprint    *string
	FleetNames     StringArray
	DeviceNames    StringArray
	RepoSpec       *JSONField[domain.RepositorySpec] `gorm:"type:jsonb"`
}
