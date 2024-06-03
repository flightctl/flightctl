package flterrors

import (
	"errors"

	"gorm.io/gorm"
)

var (
	ErrResourceIsNil                       = errors.New("resource is nil")
	ErrResourceNameIsNil                   = errors.New("metadata.name is not set")
	ErrResourceOwnerIsNil                  = errors.New("metadata.owner not set")
	ErrResourceNotFound                    = errors.New("resource not found")
	ErrUpdatingResourceWithOwnerNotAllowed = errors.New("updating the resource is not allowed because it has an owner")
	ErrDuplicateName                       = errors.New("a resource with this name already exists")

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
