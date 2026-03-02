package applications

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/flightctl/flightctl/internal/agent/device/applications/provider"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

func TestUpdateApplicationStatus_StoppedContainer(t *testing.T) {
	require := require.New(t)
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	logger := log.NewPrefixLogger("podman-monitor-test")
	monitor := &PodmanMonitor{
		log: logger,
	}

	mockVolumeManager := provider.NewMockVolumeManager(ctrl)
	mockVolumeManager.EXPECT().Status(gomock.Any()).AnyTimes()

	provider := &mockProvider{
		spec: &provider.Spec{
			Name:    "test-app",
			ID:      "test-app-id",
			AppType: v1beta1.AppTypeCompose,
			Volume:  mockVolumeManager,
		},
	}
	app := NewApplication(provider)
	workload := &Workload{
		ID:     "container-id",
		Name:   "container-name",
		Status: StatusStop,
	}
	app.AddWorkload(workload)

	event := client.PodmanEvent{
		ID:   "container-id",
		Name: "container-name",
	}

	monitor.updateApplicationStatus(app, &event, StatusExited, 0)

	updatedWorkload, exists := app.Workload("container-name")
	require.True(exists)
	require.Equal(StatusStop, updatedWorkload.Status)

	appStatus, summary, err := app.Status()
	require.NoError(err)
	require.Equal(v1beta1.ApplicationStatusError, appStatus.Status)
	require.Equal(v1beta1.ApplicationsSummaryStatusError, summary.Status)
}

// mockProvider for testing
type mockProvider struct {
	spec *provider.Spec
}

func (p *mockProvider) Spec() *provider.Spec { return p.spec }
func (p *mockProvider) Install(ctx context.Context) error { return nil }
func (p *mockProvider) Start(ctx context.Context) error   { return nil }
func (p *mockProvider) Stop(ctx context.Context) error    { return nil }
func (p *mockProvider) Remove(ctx context.Context) error  { return nil }
func (p *mockProvider) Update(ctx context.Context) error  { return nil }
