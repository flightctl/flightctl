package common

import (
	"context"
	"errors"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
)

func ValidateAndCompleteEnrollmentRequest(enrollmentRequest *v1alpha1.EnrollmentRequest) error {
	if enrollmentRequest.Status == nil {
		enrollmentRequest.Status = &v1alpha1.EnrollmentRequestStatus{
			Certificate: nil,
			Conditions:  []v1alpha1.Condition{},
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
		return server.CreateEnrollmentRequest400JSONResponse{Message: errors.Join(errs...).Error()}, nil
	}

	// verify if the enrollment request already exists, and return it with a 208 status code if it does
	if enrollmentReq, err := st.EnrollmentRequest().Get(ctx, orgId, *request.Body.Metadata.Name); err == nil {
		return server.CreateEnrollmentRequest208JSONResponse(*enrollmentReq), nil
	}

	// if the enrollment request does not exist, create it
	if err := ValidateAndCompleteEnrollmentRequest(request.Body); err != nil {
		return nil, err
	}

	result, err := st.EnrollmentRequest().Create(ctx, orgId, request.Body)
	switch err {
	case nil:
		return server.CreateEnrollmentRequest201JSONResponse(*result), nil
	case flterrors.ErrResourceIsNil:
		return server.CreateEnrollmentRequest400JSONResponse{Message: err.Error()}, nil
	default:
		return nil, err
	}
}

func ReadEnrollmentRequest(ctx context.Context, st store.Store, request server.ReadEnrollmentRequestRequestObject) (server.ReadEnrollmentRequestResponseObject, error) {
	orgId := store.NullOrgId

	result, err := st.EnrollmentRequest().Get(ctx, orgId, request.Name)
	switch err {
	case nil:
		return server.ReadEnrollmentRequest200JSONResponse(*result), nil
	case flterrors.ErrResourceNotFound:
		return server.ReadEnrollmentRequest404JSONResponse{}, nil
	default:
		return nil, err
	}
}
