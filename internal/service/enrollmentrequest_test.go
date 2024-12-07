package service

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type EnrollmentRequestStore struct {
	store.Store
	EnrollmentVal v1alpha1.EnrollmentRequest
}

func (s *EnrollmentRequestStore) EnrollmentRequest() store.EnrollmentRequest {
	return &DummyEnrollmentRequest{EnrollmentVal: s.EnrollmentVal}
}

type DummyEnrollmentRequest struct {
	store.EnrollmentRequestStore
	EnrollmentVal v1alpha1.EnrollmentRequest
}

func (s *DummyEnrollmentRequest) Get(ctx context.Context, orgId uuid.UUID, name string) (*v1alpha1.EnrollmentRequest, error) {
	if name == *s.EnrollmentVal.Metadata.Name {
		return &s.EnrollmentVal, nil
	}
	return nil, flterrors.ErrResourceNotFound
}

func (s *DummyEnrollmentRequest) UpdateStatus(ctx context.Context, orgId uuid.UUID, resource *v1alpha1.EnrollmentRequest) (*v1alpha1.EnrollmentRequest, error) {
	return nil, nil
}

func TestAlreadyApprovedEnrollmentRequestApprove(t *testing.T) {
	require := require.New(t)
	approval := &v1alpha1.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"label": "value"},
	}
	status := v1alpha1.EnrollmentRequestStatus{
		Conditions: []v1alpha1.Condition{{
			Type:    v1alpha1.EnrollmentRequestApproved,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  "ManuallyApproved",
			Message: "Approved by "}},
	}
	deviceStatus := v1alpha1.DeviceStatus{}
	device := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: util.StrToPtr("foo"),
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr:          string("TestCSR"),
			DeviceStatus: &deviceStatus,
			Labels:       &map[string]string{"labelKey": "labelValue"}},
		Status: &status,
	}
	serviceHandler := ServiceHandler{
		store:           &EnrollmentRequestStore{EnrollmentVal: device},
		callbackManager: dummyCallbackManager(),
	}
	resp, err := serviceHandler.ApproveEnrollmentRequest(context.Background(), server.ApproveEnrollmentRequestRequestObject{
		Name: "foo",
		Body: approval,
	})
	require.NoError(err)
	r, ok := resp.(server.ApproveEnrollmentRequest400JSONResponse)
	require.True(ok)
	require.Equal("Enrollment request is already approved", r.Message)
}
