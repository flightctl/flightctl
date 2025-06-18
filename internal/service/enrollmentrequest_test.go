package service

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func TestAlreadyApprovedEnrollmentRequestApprove(t *testing.T) {
	require := require.New(t)
	approval := v1alpha1.EnrollmentRequestApproval{
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
	deviceStatus := v1alpha1.NewDeviceStatus()
	device := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: lo.ToPtr("foo"),
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr:          string("TestCSR"),
			DeviceStatus: &deviceStatus,
			Labels:       &map[string]string{"labelKey": "labelValue"}},
		Status: &status,
	}
	serviceHandler := ServiceHandler{
		store:           &TestStore{},
		callbackManager: dummyCallbackManager(),
	}
	ctx := context.Background()
	_, err := serviceHandler.store.EnrollmentRequest().Create(ctx, store.NullOrgId, &device)
	require.NoError(err)
	_, stat := serviceHandler.ApproveEnrollmentRequest(ctx, "foo", approval)
	require.Equal(statusBadRequestCode, stat.Code)
	require.Equal("Enrollment request is already approved", stat.Message)
}
