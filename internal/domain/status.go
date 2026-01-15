package domain

import v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"

// Status represents an API response status.
type Status = v1beta1.Status

// Status constructors
var (
	StatusOK                      = v1beta1.StatusOK
	StatusCreated                 = v1beta1.StatusCreated
	StatusNoContent               = v1beta1.StatusNoContent
	StatusBadRequest              = v1beta1.StatusBadRequest
	StatusUnauthorized            = v1beta1.StatusUnauthorized
	StatusForbidden               = v1beta1.StatusForbidden
	StatusResourceNotFound        = v1beta1.StatusResourceNotFound
	StatusConflict                = v1beta1.StatusConflict
	StatusResourceVersionConflict = v1beta1.StatusResourceVersionConflict
	StatusInternalServerError     = v1beta1.StatusInternalServerError
	StatusNotImplemented          = v1beta1.StatusNotImplemented
	StatusTooManyRequests         = v1beta1.StatusTooManyRequests
	StatusAuthNotConfigured       = v1beta1.StatusAuthNotConfigured
)

// Status helper functions
var (
	NewSuccessStatus = v1beta1.NewSuccessStatus
	NewFailureStatus = v1beta1.NewFailureStatus
)

// PatchRequest represents an RFC 6902 JSON Patch request.
type PatchRequest = v1beta1.PatchRequest
type PatchRequestOp = v1beta1.PatchRequestOp

const (
	PatchOpAdd     = v1beta1.Add
	PatchOpRemove  = v1beta1.Remove
	PatchOpReplace = v1beta1.Replace
	PatchOpTest    = v1beta1.Test
)

// GetSwagger returns the OpenAPI spec - re-exported for service layer access due to depguard rules
var GetSwagger = v1beta1.GetSwagger
