package flterrors

import (
	"errors"
)

var (
	ErrResourceIsNil                       = errors.New("resource is nil")
	ErrResourceNameIsNil                   = errors.New("metadata.name is not set")
	ErrResourceOwnerIsNil                  = errors.New("metadata.owner not set")
	ErrResourceNotFound                    = errors.New("resource not found")
	ErrUpdatingResourceWithOwnerNotAllowed = errors.New("updating the resource is not allowed because it has an owner")
	ErrDuplicateName                       = errors.New("a resource with this name already exists")
	ErrResourceVersionConflict             = errors.New("the object has been modified; please apply your changes to the latest version and try again")
	ErrIllegalResourceVersionFormat        = errors.New("resource version does not match the required integer format")
	ErrNoRowsUpdated                       = errors.New("no rows were updated; assuming resource version was updated or resource was deleted")
	ErrFieldSelectorSyntax                 = errors.New("invalid field selector syntax")
	ErrFieldSelectorParseFailed            = errors.New("failed to parse field selector")
	ErrFieldSelectorUnknownSelector        = errors.New("unknown or unsupported selector")
	ErrLabelSelectorSyntax                 = errors.New("invalid label selector syntax")
	ErrLabelSelectorParseFailed            = errors.New("failed to parse label selector")
	ErrAnnotationSelectorSyntax            = errors.New("invalid annotation selector syntax")
	ErrAnnotationSelectorParseFailed       = errors.New("failed to parse annotation selector")

	// devices
	ErrTemplateVersionIsNil   = errors.New("spec.templateVersion not set")
	ErrInvalidTemplateVersion = errors.New("device's templateVersion is not valid")
	ErrNoRenderedVersion      = errors.New("no rendered version for device")
	ErrDecommission           = errors.New("decommissioned device cannot be created or updated")

	// csr
	ErrInvalidPEMBlock = errors.New("not a valid PEM block")
	ErrUnknownPEMType  = errors.New("unknown PEM type")
	ErrCNLength        = errors.New("CN must be at least 16 chars")
	ErrCSRParse        = errors.New("could not parse CSR")
	ErrSignature       = errors.New("signature error")
	ErrSignCert        = errors.New("error signing certificate")
	ErrEncodeCert      = errors.New("error encoding certificate")

	// certificate extensions
	ErrExtensionNotFound = errors.New("certificate extension not found")
)
