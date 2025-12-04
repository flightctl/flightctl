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
	ErrCSRInvalid      = errors.New("invalid CSR")
	ErrSignature       = errors.New("signature error")
	ErrSignCert        = errors.New("error signing certificate")
	ErrParseCert       = errors.New("could not parse certificate")
	ErrEncodeCert      = errors.New("error encoding certificate")

	// certificate extensions
	ErrExtensionNotFound = errors.New("certificate extension not found")

	// authentication/authorization
	ErrInvalidTokenClaims    = errors.New("invalid token claims")
	ErrMissingTokenClaims    = errors.New("missing required token claims")
	ErrNoMappedIdentity      = errors.New("unable to get mapped identity from context")
	ErrNoOrganizations       = errors.New("user belongs to no organizations")
	ErrAmbiguousOrganization = errors.New("user belongs to multiple organizations but no organization specified")
	ErrInvalidOrgID          = errors.New("invalid organization ID format")
	ErrNotOrgMember          = errors.New("access denied to organization")

	// authprovider
	ErrDuplicateOIDCProvider   = errors.New("an OIDC auth provider with the same issuer and clientId already exists")
	ErrDuplicateOAuth2Provider = errors.New("an OAuth2 auth provider with the same userinfoUrl and clientId already exists")
)

func IsClientAuthError(err error) bool {
	switch {
	case errors.Is(err, ErrInvalidTokenClaims):
		return true
	case errors.Is(err, ErrMissingTokenClaims):
		return true
	default:
		return false
	}
}
