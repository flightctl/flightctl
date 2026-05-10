package model

import (
	"database/sql/driver"
	"fmt"
	"strings"

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
}

func (DependencyRef) TableName() string {
	return "dependency_refs"
}

// GitDependencyProbe is the result of ListDueGitDependencies — one row per
// unique (repository_name, revision) pair that is due for polling. FleetNames
// and DeviceNames carry the fan-out targets collected via array_agg.
type GitDependencyProbe struct {
	RepositoryName string
	Revision       string
	Fingerprint    *string
	FleetNames     StringArray
	DeviceNames    StringArray
}
