package common

import (
	"context"
	"errors"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
)

func ValidateAndCompleteEnrollmentRequest(enrollmentRequest *api.EnrollmentRequest) error {
	if enrollmentRequest.Status == nil {
		enrollmentRequest.Status = &api.EnrollmentRequestStatus{
			Certificate: nil,
			Conditions:  []api.Condition{},
		}
	}
	return nil
}

func CreateEnrollmentRequest(ctx context.Context, st store.Store, request server.CreateEnrollmentRequestRequestObject) (server.CreateEnrollmentRequestResponseObject, error) {
	orgId := store.NullOrgId

	// don't set fields that are managed by the service
	request.Body.Status = nil
	NilOutManagedObjectMetaProperties(&request.Body.Metadata)

	if errs := request.Body.Validate(); len(errs) > 0 {
		return server.CreateEnrollmentRequest400JSONResponse(api.StatusBadRequest(errors.Join(errs...).Error())), nil
	}

	if err := ValidateAndCompleteEnrollmentRequest(request.Body); err != nil {
		return nil, err
	}

	result, err := st.EnrollmentRequest().Create(ctx, orgId, request.Body)
	switch {
	case err == nil:
		return server.CreateEnrollmentRequest201JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceIsNil), errors.Is(err, flterrors.ErrIllegalResourceVersionFormat):
		return server.CreateEnrollmentRequest400JSONResponse(api.StatusBadRequest(err.Error())), nil
	case errors.Is(err, flterrors.ErrDuplicateName):
		return server.CreateEnrollmentRequest409JSONResponse(api.StatusResourceVersionConflict(err.Error())), nil
	default:
		return nil, err
	}
}

func ReadEnrollmentRequest(ctx context.Context, st store.Store, request server.ReadEnrollmentRequestRequestObject) (server.ReadEnrollmentRequestResponseObject, error) {
	orgId := store.NullOrgId

	result, err := st.EnrollmentRequest().Get(ctx, orgId, request.Name)
	switch {
	case err == nil:
		return server.ReadEnrollmentRequest200JSONResponse(*result), nil
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return server.ReadEnrollmentRequest404JSONResponse(api.StatusResourceNotFound("EnrollmentRequest", request.Name)), nil
	default:
		return nil, err
	}
}
