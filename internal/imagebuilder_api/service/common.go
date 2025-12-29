package service

import (
	"errors"

	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/samber/lo"
)

const (
	MaxRecordsPerListRequest = 1000
	ImageBuildKind           = "ImageBuild"
)

// NilOutManagedObjectMetaProperties clears fields that are managed by the service
func NilOutManagedObjectMetaProperties(om *v1beta1.ObjectMeta) {
	if om == nil {
		return
	}
	om.Generation = nil
	om.Owner = nil
	om.Annotations = nil
	om.CreationTimestamp = nil
	om.DeletionTimestamp = nil
}

// prepareListParams prepares list parameters from request query parameters
func prepareListParams(cont *string, lSelector *string, fSelector *string, limit *int32) (*store.ListParams, v1beta1.Status) {
	cnt, err := store.ParseContinueString(cont)
	if err != nil {
		return nil, StatusBadRequest("failed to parse continue parameter: " + err.Error())
	}

	var fieldSelector *selector.FieldSelector
	if fSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*fSelector); err != nil {
			return nil, StatusBadRequest("failed to parse field selector: " + err.Error())
		}
	}

	var labelSelector *selector.LabelSelector
	if lSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*lSelector); err != nil {
			return nil, StatusBadRequest("failed to parse label selector: " + err.Error())
		}
	}

	listParams := &store.ListParams{
		Limit:         int(lo.FromPtr(limit)),
		Continue:      cnt,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = MaxRecordsPerListRequest
	} else if listParams.Limit > MaxRecordsPerListRequest {
		return nil, StatusBadRequest("limit cannot exceed 1000")
	} else if listParams.Limit < 0 {
		return nil, StatusBadRequest("limit cannot be negative")
	}

	return listParams, StatusOK()
}

var badRequestErrors = map[error]bool{
	flterrors.ErrResourceIsNil:                 true,
	flterrors.ErrResourceNameIsNil:             true,
	flterrors.ErrIllegalResourceVersionFormat:  true,
	flterrors.ErrFieldSelectorSyntax:           true,
	flterrors.ErrFieldSelectorParseFailed:      true,
	flterrors.ErrFieldSelectorUnknownSelector:  true,
	flterrors.ErrLabelSelectorSyntax:           true,
	flterrors.ErrLabelSelectorParseFailed:      true,
	flterrors.ErrAnnotationSelectorSyntax:      true,
	flterrors.ErrAnnotationSelectorParseFailed: true,
}

var conflictErrors = map[error]bool{
	flterrors.ErrUpdatingResourceWithOwnerNotAllowed: true,
	flterrors.ErrDuplicateName:                       true,
	flterrors.ErrNoRowsUpdated:                       true,
	flterrors.ErrResourceVersionConflict:             true,
	flterrors.ErrResourceOwnerIsNil:                  true,
}

// StoreErrorToApiStatus converts a store error to an API status
func StoreErrorToApiStatus(err error, created bool, kind string, name *string) v1beta1.Status {
	if err == nil {
		if created {
			return StatusCreated()
		}
		return StatusOK()
	}

	if errors.Is(err, flterrors.ErrResourceNotFound) {
		return StatusResourceNotFound(kind, util.DefaultIfNil(name, "none"))
	}

	for knownErr := range badRequestErrors {
		if errors.Is(err, knownErr) {
			return StatusBadRequest(err.Error())
		}
	}

	for knownErr := range conflictErrors {
		if errors.Is(err, knownErr) {
			return StatusConflict(err.Error())
		}
	}

	return StatusInternalServerError(err.Error())
}

// StatusOK returns a 200 OK status
func StatusOK() v1beta1.Status {
	return v1beta1.Status{Code: 200}
}

// StatusCreated returns a 201 Created status
func StatusCreated() v1beta1.Status {
	return v1beta1.Status{Code: 201}
}

// StatusBadRequest returns a 400 Bad Request status with the given message
func StatusBadRequest(message string) v1beta1.Status {
	return v1beta1.Status{Code: 400, Message: message}
}

// StatusNotFound returns a 404 Not Found status with the given message
func StatusNotFound(message string) v1beta1.Status {
	return v1beta1.Status{Code: 404, Message: message}
}

// StatusResourceNotFound returns a 404 status for a specific resource
func StatusResourceNotFound(kind string, name string) v1beta1.Status {
	return v1beta1.Status{Code: 404, Message: kind + " " + name + " not found"}
}

// StatusConflict returns a 409 Conflict status with the given message
func StatusConflict(message string) v1beta1.Status {
	return v1beta1.Status{Code: 409, Message: message}
}

// StatusInternalServerError returns a 500 Internal Server Error status with the given message
func StatusInternalServerError(message string) v1beta1.Status {
	return v1beta1.Status{Code: 500, Message: message}
}

// IsStatusOK returns true if the status code is in the 2xx range
func IsStatusOK(status v1beta1.Status) bool {
	return status.Code >= 200 && status.Code < 300
}
