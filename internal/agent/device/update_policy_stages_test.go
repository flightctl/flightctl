package device

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/device/applications"
	"github.com/flightctl/flightctl/internal/agent/device/dependency"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/hook"
	imagepruning "github.com/flightctl/flightctl/internal/agent/device/image_pruning"
	"github.com/flightctl/flightctl/internal/agent/device/os"
	"github.com/flightctl/flightctl/internal/agent/device/policy"
	"github.com/flightctl/flightctl/internal/agent/device/resource"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
	"go.uber.org/mock/gomock"
)

var policyWaitCases = []struct {
	name                string
	blocked             policy.Type
	prefetchPending     bool
	initialReadyVersion string
}{
	{name: "When download policy blocks a new version it should report Preparing", blocked: policy.Download, initialReadyVersion: "0"},
	{name: "When prefetch is pending it should remain Preparing", prefetchPending: true},
	{name: "When update policy blocks it should remain ReadyToUpdate", blocked: policy.Update},
}

var blockedPolicyCases = []struct {
	name           string
	firstStatusErr error
}{
	{name: "When both policies remain closed it should report Preparing once"},
	{name: "When the first status push fails it should retry Preparing", firstStatusErr: errors.ErrFailedToPushStatus},
}

func TestPolicyWaitKeepsUpdateStage(t *testing.T) {
	for _, tc := range policyWaitCases {
		t.Run(tc.name, func(t *testing.T) {
			attempts := 1
			if tc.blocked == policy.Update {
				attempts = 2
			}
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			current := newVersionedDevice("0")
			desired := newVersionedDevice("1")
			mockSpec := spec.NewMockManager(ctrl)
			mockStatus := status.NewMockManager(ctrl)
			mockResources := resource.NewMockManager(ctrl)
			mockPrefetch := dependency.NewMockPrefetchManager(ctrl)
			mockPullConfig := dependency.NewMockPullConfigResolver(ctrl)
			mockApps := applications.NewMockManager(ctrl)
			mockOS := os.NewMockManager(ctrl)
			mockHooks := hook.NewMockManager(ctrl)
			mockPruning := imagepruning.NewMockManager(ctrl)

			mockSpec.EXPECT().GetDesired(ctx).Return(desired, false, nil).Times(attempts)
			mockSpec.EXPECT().Read(spec.Current).Return(current, nil).Times(attempts)
			mockSpec.EXPECT().IsUpgrading().Return(true).AnyTimes()
			mockResources.EXPECT().BeforeUpdate(ctx, desired.Spec).Return(nil).Times(attempts)
			mockResources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(2 * attempts)
			mockStatus.EXPECT().UpdateCondition(ctx, updateStageReason(v1beta1.UpdateStatePreparing)).Return(nil)
			if tc.blocked == policy.Download {
				mockSpec.EXPECT().CheckPolicy(ctx, policy.Download, desired.Version()).Return(errors.ErrDownloadPolicyNotReady)
			} else {
				mockSpec.EXPECT().CheckPolicy(ctx, policy.Download, desired.Version()).Return(nil).Times(attempts)
				mockPullConfig.EXPECT().BeforeUpdate(desired.Spec).Times(attempts)
				mockPrefetch.EXPECT().RegisterOCICollector(mockApps).Times(attempts)
				mockSpec.EXPECT().ShouldApplyOSImageUpdate().Return(false).Times(attempts)
				mockPruning.EXPECT().RecordReferences(ctx, current, desired).Return(nil).Times(attempts)
				mockSpec.EXPECT().ShouldApplyOSImageUpdatePending(ctx).Return(false, nil).Times(attempts)
				mockOS.EXPECT().BeforeUpdate(ctx, current.Spec, desired.Spec).Return(nil).Times(attempts)
				if tc.prefetchPending {
					mockPrefetch.EXPECT().BeforeUpdate(ctx, current.Spec, desired.Spec, gomock.Any()).Return(errors.ErrPrefetchNotReady)
					mockPrefetch.EXPECT().StatusMessage(ctx).Return("prefetch pending")
					mockSpec.EXPECT().Rollback(ctx).Return(nil)
					mockStatus.EXPECT().UpdateCondition(ctx, updateStageReason(v1beta1.UpdateStatePreparing)).Return(nil)
				} else {
					mockPrefetch.EXPECT().BeforeUpdate(ctx, current.Spec, desired.Spec, gomock.Any()).Return(nil).Times(attempts)
					mockApps.EXPECT().BeforeUpdate(ctx, desired.Spec, gomock.Any()).Return(nil).Times(attempts)
					mockHooks.EXPECT().OnBeforeUpdating(ctx, current.Spec, desired.Spec).Return(nil).Times(attempts)
					mockStatus.EXPECT().UpdateCondition(ctx, updateStageReason(v1beta1.UpdateStateReadyToUpdate)).Return(nil).Times(attempts)
					mockSpec.EXPECT().CheckPolicy(ctx, policy.Update, desired.Version()).Return(errors.ErrUpdatePolicyNotReady).Times(attempts)
				}
			}

			agent := Agent{
				log:                  log.NewPrefixLogger("test"),
				readyToUpdateVersion: tc.initialReadyVersion,
				specManager:          mockSpec,
				statusManager:        mockStatus,
				resourceManager:      mockResources,
				prefetchManager:      mockPrefetch,
				pullConfigResolver:   mockPullConfig,
				appManager:           mockApps,
				osManager:            mockOS,
				hookManager:          mockHooks,
				pruningManager:       mockPruning,
			}
			for range attempts {
				agent.syncDeviceSpec(ctx)
			}
		})
	}
}

func TestBothPoliciesClosedReportsPreparing(t *testing.T) {
	for _, tc := range blockedPolicyCases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()
			desired := newVersionedDevice("1")
			mockSpec := spec.NewMockManager(ctrl)
			mockStatus := status.NewMockManager(ctrl)
			mockSpec.EXPECT().GetDesired(ctx).Return(desired, true, nil).Times(2)
			mockStatus.EXPECT().UpdateCondition(ctx, updateStageReason(v1beta1.UpdateStatePreparing)).Return(tc.firstStatusErr)
			if tc.firstStatusErr != nil {
				mockStatus.EXPECT().UpdateCondition(ctx, updateStageReason(v1beta1.UpdateStatePreparing)).Return(nil)
			}

			agent := Agent{
				log:           log.NewPrefixLogger("test"),
				specManager:   mockSpec,
				statusManager: mockStatus,
			}
			agent.syncDeviceSpec(ctx)
			agent.syncDeviceSpec(ctx)
		})
	}
}

func updateStageReason(reason v1beta1.UpdateState) gomock.Matcher {
	return gomock.Cond(func(value any) bool {
		condition, ok := value.(v1beta1.Condition)
		return ok && condition.Type == v1beta1.ConditionTypeDeviceUpdating &&
			condition.Status == v1beta1.ConditionStatusTrue && condition.Reason == string(reason)
	})
}
