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
	ErrTemplateVersionIsNil             = errors.New("spec.templateVersion not set")
	ErrUpdatingTemplateVerionNotAllowed = errors.New("updating spec.data.templateVersion not allowed")
	ErrInvalidTemplateVersion           = errors.New("device's templateVersion is not valid")
)

func ErrorFromGormError(err error) error {
	switch err {
	case nil:
		return nil
	case gorm.ErrRecordNotFound:
		return ErrResourceNotFound
	default:
		return err
	}
}
