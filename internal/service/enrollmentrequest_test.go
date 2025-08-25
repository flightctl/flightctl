package service

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

func testEnrollmentRequestPatch(require *require.Assertions, patch v1alpha1.PatchRequest) (*v1alpha1.EnrollmentRequest, v1alpha1.EnrollmentRequest, v1alpha1.Status) {
	serviceHandler, ctx, enrollmentRequest := createTestEnrollmentRequest(require, "validname", nil)
	resp, status := serviceHandler.PatchEnrollmentRequest(ctx, "validname", patch)
	require.NotEqual(statusFailedCode, status.Code)
	return resp, enrollmentRequest, status
}

func TestAlreadyApprovedEnrollmentRequestApprove(t *testing.T) {
	require := require.New(t)

	// Create enrollment request with already approved status
	approvedStatus := &v1alpha1.EnrollmentRequestStatus{
		Conditions: []v1alpha1.Condition{{
			Type:    v1alpha1.ConditionTypeEnrollmentRequestApproved,
			Status:  v1alpha1.ConditionStatusTrue,
			Reason:  "ManuallyApproved",
			Message: "Approved by "}},
	}

	serviceHandler, ctx, _ := createTestEnrollmentRequest(require, "foo", approvedStatus)

	approval := v1alpha1.EnrollmentRequestApproval{
		Approved: true,
		Labels:   &map[string]string{"label": "value"},
	}

	_, stat := serviceHandler.ApproveEnrollmentRequest(ctx, "foo", approval)
	require.Equal(statusBadRequestCode, stat.Code)
	require.Equal("Enrollment request is already approved", stat.Message)

	event, _ := serviceHandler.store.Event().List(ctx, store.NullOrgId, store.ListParams{})
	require.Len(event.Items, 0)
}

func TestNotFoundReplaceEnrollmentRequestStatus(t *testing.T) {
	require := require.New(t)
	serviceHandler := ServiceHandler{
		store: &TestStore{},
	}
	ctx := context.Background()

	invalidER := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name: lo.ToPtr("NonExistingName"),
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr: "TestCSR",
		},
	}

	_, status := serviceHandler.ReplaceEnrollmentRequestStatus(ctx, "InvalidName", invalidER)

	require.Equal(statusNotFoundCode, status.Code)
}

func TestEnrollmentRequestPatchInvalidRequests(t *testing.T) {
	require := require.New(t)

	testCases := []struct {
		name         string
		patchRequest v1alpha1.PatchRequest
	}{
		{
			name: "replace name with invalid value",
			patchRequest: v1alpha1.PatchRequest{
				{Op: "replace", Path: "/metadata/name", Value: func() *interface{} { var v interface{} = "InvalidName"; return &v }()},
			},
		},
		{
			name: "remove name field",
			patchRequest: v1alpha1.PatchRequest{
				{Op: "remove", Path: "/metadata/name"},
			},
		},
		{
			name: "replace kind field",
			patchRequest: v1alpha1.PatchRequest{
				{Op: "replace", Path: "/kind", Value: func() *interface{} { var v interface{} = "SomeOtherKind"; return &v }()},
			},
		},
		{
			name: "remove kind field",
			patchRequest: v1alpha1.PatchRequest{
				{Op: "remove", Path: "/kind"},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, status := testEnrollmentRequestPatch(require, tc.patchRequest)
			verifyERPatchFailed(require, status)
		})
	}
}

func verifyERPatchFailed(require *require.Assertions, status v1alpha1.Status) {
	require.Equal(statusBadRequestCode, status.Code)
}

func createTestEnrollmentRequest(require *require.Assertions, name string, status *v1alpha1.EnrollmentRequestStatus) (*ServiceHandler, context.Context, v1alpha1.EnrollmentRequest) {
	deviceStatus := v1alpha1.NewDeviceStatus()
	enrollmentRequest := v1alpha1.EnrollmentRequest{
		ApiVersion: "v1",
		Kind:       "EnrollmentRequest",
		Metadata: v1alpha1.ObjectMeta{
			Name:   lo.ToPtr(name),
			Labels: &map[string]string{"labelKey": "labelValue"},
		},
		Spec: v1alpha1.EnrollmentRequestSpec{
			Csr:          "TestCSR",
			DeviceStatus: &deviceStatus,
		},
		Status: status,
	}
	serviceHandler := ServiceHandler{
		store: &TestStore{},
	}
	ctx := context.Background()
	_, err := serviceHandler.store.EnrollmentRequest().Create(ctx, store.NullOrgId, &enrollmentRequest, nil)
	require.NoError(err)
	return &serviceHandler, ctx, enrollmentRequest
}
