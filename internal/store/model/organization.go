package model

import (
	"time"

	"github.com/google/uuid"
)

type Organization struct {
	ID uuid.UUID `gorm:"type:uuid;primary_key"`

	// Whether this is the default organization.
	// There should only ever be one default organization.
	Default bool `json:"default"`

	// External identifier of the organization in the configured IdP.
	ExternalID string `json:"external_id"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
