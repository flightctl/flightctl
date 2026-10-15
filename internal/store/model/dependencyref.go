package model

import (
	"database/sql/driver"
	"fmt"
	"strings"

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

	RefType             string // "git", "http", "secret"
	RepositoryName      *string
	Revision            *string
	HTTPSuffix          *string
	SecretName          *string
	SecretNamespace     *string
	ConfigProviderName  string
	ConfigProviderNames StringArray `gorm:"type:text[]"`
}

func (DependencyRef) TableName() string {
	return "dependency_refs"
}

// AddConfigProviderName records a provider while retaining the legacy single
// provider field used by older dependency-ref rows.
func (r *DependencyRef) AddConfigProviderName(name string) {
	if name == "" {
		return
	}
	if r.ConfigProviderName == "" {
		r.ConfigProviderName = name
	}
	add := func(providerName string) {
		for _, existing := range r.ConfigProviderNames {
			if existing == providerName {
				return
			}
		}
		r.ConfigProviderNames = append(r.ConfigProviderNames, providerName)
	}
	add(r.ConfigProviderName)
	add(name)
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

// GitDependencyRenderedTarget associates a rendered fingerprint with the
// device that produced it. DeviceLevel distinguishes a direct device ref from
// a fleet ref expanded to its owned devices for first-probe comparisons.
type GitDependencyRenderedTarget struct {
	FleetName   string `json:"fleetName"`
	DeviceName  string `json:"deviceName"`
	Fingerprint string `json:"fingerprint"`
	DeviceLevel bool   `json:"deviceLevel"`
}

// GitDependencyProbe is the result of ListDueGitDependencies — one row per
// unique (repository_name, revision) pair that is due for polling. FleetNames
// and DeviceNames carry the fan-out targets collected via array_agg, while
// RenderedTargets preserve the device/fingerprint association used for a
// first-probe comparison.
// RepoSpec carries the repository's JSONB spec so callers can extract the
// URL and auth without a separate GetRepository round-trip.
type GitDependencyProbe struct {
	RepositoryName  string
	Revision        string
	Fingerprint     *string
	FleetNames      StringArray
	DeviceNames     StringArray
	RenderedTargets *JSONField[[]GitDependencyRenderedTarget] `gorm:"type:jsonb"`
	RepoSpec        *JSONField[domain.RepositorySpec]         `gorm:"type:jsonb"`
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
