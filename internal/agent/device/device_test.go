package device

import (
	"context"
	"fmt"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/agent/device/errors"
	"github.com/flightctl/flightctl/internal/agent/device/spec"
	"github.com/flightctl/flightctl/internal/agent/device/status"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestEnqueue(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockSpecManager := spec.NewMockManager(ctrl)
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	require := require.New(t)
	agent := Agent{
		log:                    log,
		specManager:            mockSpecManager,
		currentRenderedVersion: "1",
		queue:                  spec.NewQueue(log, 3, 10),
	}
	mockSpecManager.EXPECT().GetDesired(gomock.Any(), agent.currentRenderedVersion).Return(newTestSpec("1"), nil)
	err := agent.enqueue(context.Background())
	require.NoError(err)
	require.Equal(agent.queue.Len(), 1)

}

func TestFetchDeviceSpecNoRetry(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	mockSpecManager := spec.NewMockManager(ctrl)
	mockStatusManager := status.NewMockManager(ctrl)
	log := log.NewPrefixLogger("test")
	log.SetLevel(logrus.DebugLevel)

	agent := Agent{
		log:                    log,
		specManager:            mockSpecManager,
		statusManager:          mockStatusManager,
		currentRenderedVersion: "1",
		queue:                  spec.NewQueue(log, 3, 10),
	}

	// desired spec version 2 fails with no retry
	desiredSpecVersion := int64(2)
	mockSpecManager.EXPECT().GetDesired(gomock.Any(), agent.currentRenderedVersion).Return(newTestSpec(fmt.Sprintf("%d", desiredSpecVersion)), nil).Times(2)
	mockStatusManager.EXPECT().Update(gomock.Any(), gomock.Any()).Return(nil, nil)

	ctx := context.Background()
	agent.fetchDeviceSpec(ctx, testSyncNoRetry)
	require.True(agent.queue.IsVersionFailed(desiredSpecVersion))
	require.Zero(agent.queue.Len())
	err := agent.enqueue(ctx)
	require.NoError(err)
	require.True(agent.queue.IsVersionFailed(desiredSpecVersion))
	require.Zero(agent.queue.Len())
}

func testSyncNoRetry(ctx context.Context, desired *v1alpha1.RenderedDeviceSpec) error {
	return errors.ErrNoRetry
}

func newTestSpec(version string) *v1alpha1.RenderedDeviceSpec {
	return &v1alpha1.RenderedDeviceSpec{
		RenderedVersion: version,
	}
}
