package status

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// TestInvalidateLastStatus_CausesNextSyncToPush verifies that when InvalidateLastStatus is called,
// the next Sync pushes status again (UpdateDeviceStatus called twice total). We call InvalidateLastStatus
// ourselves here because this test is about the StatusManager's behavior. The condition under which
// it gets called (GetRenderedDevice returns 200 with ConflictPaused) is tested in spec/publisher_test.go
// (TestDevicePublisher_ConflictPausedCallback).
func TestInvalidateLastStatus_CausesNextSyncToPush(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	deviceName := "test-device"
	ctx := context.Background()
	mockClient := client.NewMockManagement(ctrl)
	mockExporter := NewMockExporter(ctrl)

	mgr := NewManager(deviceName, log.NewPrefixLogger(""))
	mgr.SetClient(mockClient)
	mgr.RegisterStatusExporter(mockExporter)

	mockExporter.EXPECT().Status(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).Times(2)
	mockClient.EXPECT().UpdateDeviceStatus(gomock.Any(), deviceName, gomock.Any()).Return(nil).Times(2)

	require.NoError(mgr.Sync(ctx))
	mgr.InvalidateLastStatus()
	require.NoError(mgr.Sync(ctx))
}
