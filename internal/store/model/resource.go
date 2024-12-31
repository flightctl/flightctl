package model

import (
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

type Resource struct {
	// Uniquely identifies the tenant the resource belongs to.
	// Assigned by IAM. Immutable.
	OrgID uuid.UUID `gorm:"type:uuid;primary_key;index:owner_idx,priority:2"`

	// Uniquely identifies the resource within a tenant and schema.
	// Depending on the schema (kind), assigned by the device management system or the crypto identity of the device (public key). Immutable.
	// This may become a URN later, so it's important API users treat this as an opaque handle.
	Name string `gorm:"primary_key;" selector:"metadata.name"`

	// User-defined name, if non-null used in the UI as a more human-friendly alias to the resource ID.
	// DisplayName string

	// The "kind/name" of the resource owner of this resource.
	Owner *string `gorm:"index:owner_idx,priority:1" selector:"metadata.owner"`

	// Labels associated with the resource, used for selecting and querying objects.
	// Labels are stored as a JSONB object, supporting flexible indexing and querying capabilities.
	Labels JSONMap[string, string] `gorm:"type:jsonb" selector:"metadata.labels,hidden,private"`

	// Annotations associated with the resource, used for storing additional metadata.
	// Similar to labels, annotations are stored as a JSONB object to support flexible indexing and querying.
	Annotations JSONMap[string, string] `gorm:"type:jsonb" selector:"metadata.annotations,hidden,private"`

	Generation      *int64
	ResourceVersion *int64
	CreatedAt       time.Time `selector:"metadata.creationTimestamp"`
	UpdatedAt       time.Time
	DeletedAt       gorm.DeletedAt `gorm:"index"`
}

func (r *Resource) BeforeCreate(tx *gorm.DB) error {
	if len(r.Name) == 0 {
		r.Name = uuid.New().String()
	}
	return nil
}
