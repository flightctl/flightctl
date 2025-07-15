package model

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

var (
	ErrDefaultOrganizationExists = errors.New("default organization already exists")
)

type Organization struct {
	ID uuid.UUID `gorm:"type:uuid;primary_key"`

	// Whether this is the default organization.
	// There should only ever be one default organization.
	IsDefault bool `gorm:"column:is_default" json:"is_default"`

	// External identifier of the organization in the configured IdP.
	ExternalID string `json:"external_id"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (o *Organization) BeforeSave(tx *gorm.DB) error {
	if o.IsDefault {
		// Check if another default organization already exists
		var count int64
		err := tx.Model(&Organization{}).Where("is_default = ? AND id != ?", true, o.ID).Count(&count).Error
		if err != nil {
			return err
		}
		if count > 0 {
			return ErrDefaultOrganizationExists
		}
	}
	return nil
}
