package device

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

const statusVersionTestDevice = "status-version-test"

var replaceVersionCases = []struct {
	name       string
	reportedRV *string
	advanceRV  bool
	wantCode   int32
}{
	{name: "When the report has the current version it should be accepted", reportedRV: lo.ToPtr("3"), wantCode: http.StatusOK},
	{name: "When the report has an older version it should be rejected", reportedRV: lo.ToPtr("2"), wantCode: http.StatusConflict},
	{name: "When the stored version advances during the write it should be rejected", reportedRV: lo.ToPtr("3"), advanceRV: true, wantCode: http.StatusConflict},
	{name: "When a legacy report has no version it should remain accepted", wantCode: http.StatusOK},
}

func TestReplaceStatusVersionPrecondition(t *testing.T) {
	for _, tc := range replaceVersionCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			orgID := uuid.New()
			st, ev, svc := newTestHandler()
			createStatusVersionDevice(t, st, orgID)
			if tc.advanceRV {
				advanceVersionBeforeStatusWrite(st)
			}

			incoming := statusVersionReport(tc.reportedRV, domain.UpdateStatePreparing)
			_, result := svc.ReplaceDeviceStatus(ctx, orgID, statusVersionTestDevice, incoming, false)
			require.Equal(t, tc.wantCode, result.Code)
			stored, err := st.device.Get(ctx, orgID, statusVersionTestDevice)
			require.NoError(t, err)
			condition := domain.FindStatusCondition(stored.Status.Conditions, domain.ConditionTypeDeviceUpdating)
			require.NotNil(t, condition)
			if tc.wantCode == http.StatusConflict {
				require.Equal(t, string(domain.UpdateStateApplyingUpdate), condition.Reason)
				require.Empty(t, ev.created, "rejected status must not create an event")
			} else {
				require.Equal(t, string(domain.UpdateStatePreparing), condition.Reason)
			}
		})
	}
}

var patchVersionCases = []struct {
	name       string
	reportedRV *string
	advanceRV  bool
	wantCode   int32
}{
	{name: "When the patch has the current version it should be accepted", reportedRV: lo.ToPtr("3"), wantCode: http.StatusOK},
	{name: "When the patch has an older version it should be rejected", reportedRV: lo.ToPtr("2"), wantCode: http.StatusConflict},
	{name: "When the stored version advances during a patch it should be rejected", reportedRV: lo.ToPtr("3"), advanceRV: true, wantCode: http.StatusConflict},
	{name: "When a legacy patch has no version it should remain accepted", wantCode: http.StatusOK},
}

func TestPatchStatusVersionPrecondition(t *testing.T) {
	for _, tc := range patchVersionCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			orgID := uuid.New()
			st, ev, svc := newTestHandler()
			createStatusVersionDevice(t, st, orgID)
			if tc.advanceRV {
				advanceVersionBeforeStatusWrite(st)
			}

			patch := statusVersionPatch(tc.reportedRV, domain.UpdateStatePreparing)
			_, result := svc.PatchDeviceStatus(ctx, orgID, statusVersionTestDevice, patch, false)
			require.Equal(t, tc.wantCode, result.Code)
			stored, err := st.device.Get(ctx, orgID, statusVersionTestDevice)
			require.NoError(t, err)
			condition := domain.FindStatusCondition(stored.Status.Conditions, domain.ConditionTypeDeviceUpdating)
			require.NotNil(t, condition)
			if tc.wantCode == http.StatusConflict {
				require.Equal(t, string(domain.UpdateStateApplyingUpdate), condition.Reason)
				require.Empty(t, ev.created, "rejected status must not create an event")
			} else {
				require.Equal(t, string(domain.UpdateStatePreparing), condition.Reason)
			}
		})
	}
}

func TestRepeatedCurrentStatusDoesNotRepeatEvent(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	st, ev, svc := newTestHandler()
	createStatusVersionDevice(t, st, orgID)

	report := statusVersionReport(lo.ToPtr("3"), domain.UpdateStatePreparing)
	_, result := svc.ReplaceDeviceStatus(ctx, orgID, statusVersionTestDevice, report, false)
	require.Equal(t, int32(http.StatusOK), result.Code)
	firstEvents := len(ev.created)
	require.Positive(t, firstEvents)

	current, err := st.device.Get(ctx, orgID, statusVersionTestDevice)
	require.NoError(t, err)
	report.Metadata.ResourceVersion = current.Metadata.ResourceVersion
	_, result = svc.ReplaceDeviceStatus(ctx, orgID, statusVersionTestDevice, report, false)
	require.Equal(t, int32(http.StatusOK), result.Code)
	require.Len(t, ev.created, firstEvents)
}

func createStatusVersionDevice(t *testing.T, st *fakeStore, orgID uuid.UUID) {
	t.Helper()
	_, err := st.device.Create(context.Background(), orgID, &domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(statusVersionTestDevice), ResourceVersion: lo.ToPtr("3")},
		Spec:     &domain.DeviceSpec{},
		Status:   lo.ToPtr(statusVersionStatus(domain.UpdateStateApplyingUpdate)),
	}, nil)
	require.NoError(t, err)
}

func statusVersionStatus(reason domain.UpdateState) domain.DeviceStatus {
	status := domain.NewDeviceStatus()
	status.Conditions = []domain.Condition{{
		Type: domain.ConditionTypeDeviceUpdating, Status: domain.ConditionStatusTrue,
		Reason: string(reason), Message: string(reason), LastTransitionTime: time.Now().UTC(),
	}}
	return status
}

func statusVersionReport(rv *string, reason domain.UpdateState) domain.Device {
	return domain.Device{
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(statusVersionTestDevice), ResourceVersion: rv},
		Status:   lo.ToPtr(statusVersionStatus(reason)),
	}
}

func statusVersionPatch(rv *string, reason domain.UpdateState) domain.PatchRequest {
	patch := domain.PatchRequest{}
	if rv != nil {
		patch = append(patch, domain.PatchRequest{{Op: domain.PatchOpTest, Path: "/metadata/resourceVersion", Value: *rv}}...)
	}
	return append(patch, domain.PatchRequest{{Op: domain.PatchOpReplace, Path: "/status/conditions", Value: statusVersionStatus(reason).Conditions}}...)
}

func advanceVersionBeforeStatusWrite(st *fakeStore) {
	st.device.beforeMutate = func(_ int) {
		st.device.devices[statusVersionTestDevice].Metadata.ResourceVersion = lo.ToPtr("4")
	}
}
