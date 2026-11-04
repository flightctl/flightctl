package tasks

import (
	"context"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestLabelReconciliationRequestWhenDispatchedItShouldOnlyReconcileLabels(t *testing.T) {
	ctx, log := context.Background(), logrus.New()
	event := createTestEvent(domain.DeviceKind, domain.EventReasonDeviceLabelReconciliationRequested, "device")
	require.True(t, shouldReconcileDeviceLabels(ctx, event))
	require.False(t, shouldReconcileDeviceOwnership(ctx, event, log))
	require.False(t, shouldRolloutFleet(ctx, event, log))
	require.False(t, shouldValidateFleet(ctx, event, log))
	require.False(t, shouldRenderDevice(ctx, event, log))
	require.False(t, shouldUpdateRepositoryReferers(ctx, event, log))
	require.False(t, shouldPopulateDependencyRefs(ctx, event, log))
	require.False(t, shouldReconcileFleetApplicationLifecycle(ctx, event, log))
}

func TestLaunchConsumersWhenStartingItShouldAllocateOneLoopPerQueue(t *testing.T) {
	ctrl := gomock.NewController(t)
	provider := queues.NewMockProvider(ctrl)
	for _, name := range []string{consts.TaskQueue, consts.DeviceLabelSyncTaskQueue} {
		consumer := queues.NewMockQueueConsumer(ctrl)
		provider.EXPECT().NewQueueConsumer(gomock.Any(), name).Return(consumer, nil).Times(1)
		consumer.EXPECT().Consume(gomock.Any(), gomock.Any()).Return(nil).Times(1)
	}
	require.NoError(t, LaunchConsumers(context.Background(), provider, TaskConsumer{}, 1, 1))
}

func TestLaunchConsumersWhenLabelSyncStartupFailsItShouldReturnTheError(t *testing.T) {
	for _, failConsume := range []bool{false, true} {
		name := "When label sync consumer creation fails it should fail startup"
		if failConsume {
			name = "When label sync consumption fails it should fail startup"
		}
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			provider := queues.NewMockProvider(ctrl)
			main := queues.NewMockQueueConsumer(ctrl)
			provider.EXPECT().NewQueueConsumer(gomock.Any(), consts.TaskQueue).Return(main, nil)
			main.EXPECT().Consume(gomock.Any(), gomock.Any()).Return(nil)
			failure := errors.New("label sync consumer unavailable")
			if failConsume {
				labelSync := queues.NewMockQueueConsumer(ctrl)
				provider.EXPECT().NewQueueConsumer(gomock.Any(), consts.DeviceLabelSyncTaskQueue).Return(labelSync, nil)
				labelSync.EXPECT().Consume(gomock.Any(), gomock.Any()).Return(failure)
			} else {
				provider.EXPECT().NewQueueConsumer(gomock.Any(), consts.DeviceLabelSyncTaskQueue).Return(nil, failure)
			}
			require.ErrorIs(t, LaunchConsumers(context.Background(), provider, TaskConsumer{}, 1, 1), failure)
		})
	}
}
