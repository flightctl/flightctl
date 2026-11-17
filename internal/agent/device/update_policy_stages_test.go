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
	name            string
	blocked         policy.Type
	prefetchPending bool
}{
	{name: "When download policy blocks it should remain Preparing", blocked: policy.Download},
	{name: "When prefetch is pending it should remain Preparing", prefetchPending: true},
	{name: "When update policy blocks it should remain ReadyToUpdate", blocked: policy.Update},
}

func TestPolicyWaitKeepsUpdateStage(t *testing.T) {
	for _, tc := range policyWaitCases {
		t.Run(tc.name, func(t *testing.T) {
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

			mockSpec.EXPECT().GetDesired(ctx).Return(desired, false, nil)
			mockSpec.EXPECT().Read(spec.Current).Return(current, nil)
			mockSpec.EXPECT().IsUpgrading().Return(true).AnyTimes()
			mockResources.EXPECT().BeforeUpdate(ctx, desired.Spec).Return(nil)
			mockResources.EXPECT().IsCriticalAlert(gomock.Any()).Return(false).Times(2)
			mockStatus.EXPECT().UpdateCondition(ctx, updateStageReason(v1beta1.UpdateStatePreparing)).Return(nil)
			if tc.blocked == policy.Download {
				mockSpec.EXPECT().CheckPolicy(ctx, policy.Download, desired.Version()).Return(errors.ErrDownloadPolicyNotReady)
			} else {
				mockSpec.EXPECT().CheckPolicy(ctx, policy.Download, desired.Version()).Return(nil)
				mockPullConfig.EXPECT().BeforeUpdate(desired.Spec)
				mockPrefetch.EXPECT().RegisterOCICollector(mockApps)
				mockSpec.EXPECT().ShouldApplyOSImageUpdate().Return(false)
				mockPruning.EXPECT().RecordReferences(ctx, current, desired).Return(nil)
				mockSpec.EXPECT().ShouldApplyOSImageUpdatePending(ctx).Return(false, nil)
				mockOS.EXPECT().BeforeUpdate(ctx, current.Spec, desired.Spec).Return(nil)
				if tc.prefetchPending {
					mockPrefetch.EXPECT().BeforeUpdate(ctx, current.Spec, desired.Spec, gomock.Any()).Return(errors.ErrPrefetchNotReady)
					mockPrefetch.EXPECT().StatusMessage(ctx).Return("prefetch pending")
					mockSpec.EXPECT().Rollback(ctx).Return(nil)
					mockStatus.EXPECT().UpdateCondition(ctx, updateStageReason(v1beta1.UpdateStatePreparing)).Return(nil)
				} else {
					mockPrefetch.EXPECT().BeforeUpdate(ctx, current.Spec, desired.Spec, gomock.Any()).Return(nil)
					mockApps.EXPECT().BeforeUpdate(ctx, desired.Spec, gomock.Any()).Return(nil)
					mockHooks.EXPECT().OnBeforeUpdating(ctx, current.Spec, desired.Spec).Return(nil)
					mockStatus.EXPECT().UpdateCondition(ctx, updateStageReason(v1beta1.UpdateStateReadyToUpdate)).Return(nil)
					mockSpec.EXPECT().CheckPolicy(ctx, policy.Update, desired.Version()).Return(errors.ErrUpdatePolicyNotReady)
				}
			}

			agent := Agent{
				log:                log.NewPrefixLogger("test"),
				specManager:        mockSpec,
				statusManager:      mockStatus,
				resourceManager:    mockResources,
				prefetchManager:    mockPrefetch,
				pullConfigResolver: mockPullConfig,
				appManager:         mockApps,
				osManager:          mockOS,
				hookManager:        mockHooks,
				pruningManager:     mockPruning,
			}
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
