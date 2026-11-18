package status

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const statusVersionTestName = "status-version-test"

func TestStatusReportsUseResourceVersion(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	management := client.NewMockManagement(ctrl)
	manager := NewManager(statusVersionTestName, log.NewPrefixLogger("test"))
	manager.SetClient(management)
	var reportedVersions []string

	management.EXPECT().GetRenderedDevice(gomock.Any(), statusVersionTestName, nil).Return(statusVersionDevice("5"), http.StatusOK, nil)
	management.EXPECT().UpdateDeviceStatus(gomock.Any(), statusVersionTestName, gomock.Any()).Do(captureStatusVersion(&reportedVersions)).Return(nil)
	management.EXPECT().UpdateDeviceStatus(gomock.Any(), statusVersionTestName, gomock.Any()).Do(captureStatusVersion(&reportedVersions)).Return(nil)

	require.NoError(t, manager.UpdateCondition(ctx, statusVersionCondition(v1beta1.UpdateStatePreparing)))
	require.NoError(t, manager.UpdateCondition(ctx, statusVersionCondition(v1beta1.UpdateStateReadyToUpdate)))
	require.Equal(t, []string{"5", "6"}, reportedVersions)
}

func TestStatusReportRefreshesAfterVersionConflict(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	management := client.NewMockManagement(ctrl)
	manager := NewManager(statusVersionTestName, log.NewPrefixLogger("test"))
	manager.SetClient(management)
	var reportedVersions []string

	management.EXPECT().GetRenderedDevice(gomock.Any(), statusVersionTestName, nil).Return(statusVersionDevice("5"), http.StatusOK, nil)
	management.EXPECT().UpdateDeviceStatus(gomock.Any(), statusVersionTestName, gomock.Any()).Do(captureStatusVersion(&reportedVersions)).Return(client.ErrStatusConflict)
	management.EXPECT().GetRenderedDevice(gomock.Any(), statusVersionTestName, nil).Return(statusVersionDevice("8"), http.StatusOK, nil)
	management.EXPECT().UpdateDeviceStatus(gomock.Any(), statusVersionTestName, gomock.Any()).Do(captureStatusVersion(&reportedVersions)).Return(nil)

	require.Error(t, manager.UpdateCondition(ctx, statusVersionCondition(v1beta1.UpdateStatePreparing)))
	require.NoError(t, manager.UpdateCondition(ctx, statusVersionCondition(v1beta1.UpdateStateApplyingUpdate)))
	require.Equal(t, []string{"5", "8"}, reportedVersions)
	require.Equal(t, string(v1beta1.UpdateStateApplyingUpdate), v1beta1.FindStatusCondition(manager.Get(ctx).Conditions, v1beta1.ConditionTypeDeviceUpdating).Reason)
}

func TestStatusReportDuringRenderedGate(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()
	management := client.NewMockManagement(ctrl)
	manager := NewManager(statusVersionTestName, log.NewPrefixLogger("test"))
	manager.SetClient(management)
	var reportedVersions []string

	management.EXPECT().GetRenderedDevice(gomock.Any(), statusVersionTestName, nil).Return(nil, http.StatusConflict, nil)
	management.EXPECT().UpdateDeviceStatus(gomock.Any(), statusVersionTestName, gomock.Any()).Do(captureStatusVersion(&reportedVersions)).Return(nil)

	require.NoError(t, manager.UpdateCondition(ctx, v1beta1.Condition{
		Type: v1beta1.ConditionTypeDeviceEnrollmentHooks, Status: v1beta1.ConditionStatusTrue,
		Reason: "Succeeded", Message: "Enrollment hooks completed",
	}))
	require.Empty(t, reportedVersions)
}

func statusVersionDevice(version string) *v1beta1.Device {
	return &v1beta1.Device{Metadata: v1beta1.ObjectMeta{ResourceVersion: lo.ToPtr(version)}}
}

func statusVersionCondition(reason v1beta1.UpdateState) v1beta1.Condition {
	return v1beta1.Condition{
		Type: v1beta1.ConditionTypeDeviceUpdating, Status: v1beta1.ConditionStatusTrue,
		Reason: string(reason), Message: string(reason),
	}
}

func captureStatusVersion(versions *[]string) func(context.Context, string, v1beta1.Device, ...any) {
	return func(_ context.Context, _ string, device v1beta1.Device, _ ...any) {
		if device.Metadata.ResourceVersion != nil {
			*versions = append(*versions, *device.Metadata.ResourceVersion)
		}
	}
}
