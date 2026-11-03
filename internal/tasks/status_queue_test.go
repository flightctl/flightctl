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

func TestStatusOnlyEventWhenDispatchedItShouldOnlyReconcileLabels(t *testing.T) {
	ctx, log := context.Background(), logrus.New()
	event := createTestEventWithDetails(domain.DeviceKind, domain.EventReasonResourceUpdated, "device", createResourceUpdatedDetails(t, domain.UpdatedFieldStatus))
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
	for _, name := range []string{consts.TaskQueue, consts.DeviceStatusTaskQueue} {
		consumer := queues.NewMockQueueConsumer(ctrl)
		provider.EXPECT().NewQueueConsumer(gomock.Any(), name).Return(consumer, nil).Times(1)
		consumer.EXPECT().Consume(gomock.Any(), gomock.Any()).Return(nil).Times(1)
	}
	require.NoError(t, LaunchConsumers(context.Background(), provider, TaskConsumer{}, 1, 1))
}

func TestLaunchConsumersWhenStatusStartupFailsItShouldReturnTheError(t *testing.T) {
	for _, failConsume := range []bool{false, true} {
		name := "When status consumer creation fails it should fail startup"
		if failConsume {
			name = "When status consumption fails it should fail startup"
		}
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)
			provider := queues.NewMockProvider(ctrl)
			main := queues.NewMockQueueConsumer(ctrl)
			provider.EXPECT().NewQueueConsumer(gomock.Any(), consts.TaskQueue).Return(main, nil)
			main.EXPECT().Consume(gomock.Any(), gomock.Any()).Return(nil)
			failure := errors.New("status consumer unavailable")
			if failConsume {
				status := queues.NewMockQueueConsumer(ctrl)
				provider.EXPECT().NewQueueConsumer(gomock.Any(), consts.DeviceStatusTaskQueue).Return(status, nil)
				status.EXPECT().Consume(gomock.Any(), gomock.Any()).Return(failure)
			} else {
				provider.EXPECT().NewQueueConsumer(gomock.Any(), consts.DeviceStatusTaskQueue).Return(nil, failure)
			}
			require.ErrorIs(t, LaunchConsumers(context.Background(), provider, TaskConsumer{}, 1, 1), failure)
		})
	}
}
