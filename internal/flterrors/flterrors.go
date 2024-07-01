package flterrors

import (
	"errors"

	"gorm.io/gorm"
)

var (
	ErrResourceIsNil                       = errors.New("object is nil")
	ErrResourceNameIsNil                   = errors.New("metadata.name is not set")
	ErrResourceOwnerIsNil                  = errors.New("metadata.owner not set")
	ErrResourceNotFound                    = errors.New("object not found")
	ErrUpdatingResourceWithOwnerNotAllowed = errors.New("updating the object is not allowed because it has an owner")
	ErrDuplicateName                       = errors.New("an object with this name already exists")
	ErrResourceVersionConflict             = errors.New("the object has been modified; please apply your changes to the latest version and try again")

	// devices
	ErrTemplateVersionIsNil   = errors.New("spec.templateVersion not set")
	ErrInvalidTemplateVersion = errors.New("device's templateVersion is not valid")
	ErrNoRenderedVersion      = errors.New("no rendered version for device")
)

func ErrorFromGormError(err error) error {
	switch err {
	case nil:
		return nil
	case gorm.ErrRecordNotFound:
		return ErrResourceNotFound
	case gorm.ErrDuplicatedKey:
		return ErrDuplicateName
	default:
		return err
	}
}
