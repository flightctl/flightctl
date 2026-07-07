package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

// newLifecycleTestFleet creates a fleet whose device template declares a single container
// application named appName, registers it in a fresh ServiceHandler backed by TestStore, and
// returns the handler along with the org and fleet name to use in calls.
func newLifecycleTestFleet(t *testing.T, appName string) (h *ServiceHandler, orgId uuid.UUID, fleetName string) {
	t.Helper()
	require := require.New(t)

	containerApp := domain.ContainerApplication{
		AppType: domain.AppTypeContainer,
		Name:    lo.ToPtr(appName),
		Image:   "quay.io/test/app:v1",
	}
	var app domain.ApplicationProviderSpec
	require.NoError(app.FromContainerApplication(containerApp))

	fleetName = "fleet-1"
	fleet := domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr(fleetName),
		},
	}
	fleet.Spec.Template.Spec.Applications = &[]domain.ApplicationProviderSpec{app}

	ts := &TestStore{}
	wc := &DummyWorkerClient{}
	h = &ServiceHandler{
		eventHandler: NewEventHandler(ts, wc, log.InitLogs()),
		store:        ts,
		workerClient: wc,
	}
	orgId = uuid.New()
	_, err := h.store.Fleet().Create(context.Background(), orgId, &fleet, nil)
	require.NoError(err)

	return h, orgId, fleetName
}

func TestStopStartFleetApplication(t *testing.T) {
	ctx := context.Background()

	t.Run("StopFleetApplication sets desiredState=stopped without touching the declarative template", func(t *testing.T) {
		require := require.New(t)
		h, orgId, fleetName := newLifecycleTestFleet(t, "app-1")

		fleet, status := h.StopFleetApplication(ctx, orgId, fleetName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		require.NotNil(fleet.Metadata.Annotations)
		overrides := decodeLifecycleOverrides(t, (*fleet.Metadata.Annotations)[domain.FleetAnnotationApplicationLifecycle])
		require.NotNil(overrides["app-1"].DesiredState)
		require.Equal(domain.ApplicationDesiredStateStopped, *overrides["app-1"].DesiredState)
		require.NotNil(overrides["app-1"].DesiredStateVersion)

		// The declarative template itself is untouched; the default lives only in the annotation.
		require.Equal(domain.ApplicationDesiredStateRunning, (*fleet.Spec.Template.Spec.Applications)[0].GetDesiredState())
	})

	t.Run("StartFleetApplication sets desiredState=running", func(t *testing.T) {
		require := require.New(t)
		h, orgId, fleetName := newLifecycleTestFleet(t, "app-1")

		_, status := h.StopFleetApplication(ctx, orgId, fleetName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)

		fleet, status := h.StartFleetApplication(ctx, orgId, fleetName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		overrides := decodeLifecycleOverrides(t, (*fleet.Metadata.Annotations)[domain.FleetAnnotationApplicationLifecycle])
		require.NotNil(overrides["app-1"].DesiredState)
		require.Equal(domain.ApplicationDesiredStateRunning, *overrides["app-1"].DesiredState)
		require.NotNil(overrides["app-1"].DesiredStateVersion)
	})

	t.Run("StartFleetApplication issued after StopFleetApplication has a strictly newer version", func(t *testing.T) {
		require := require.New(t)
		h, orgId, fleetName := newLifecycleTestFleet(t, "app-1")

		_, status := h.StopFleetApplication(ctx, orgId, fleetName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		stoppedFleet, err := h.store.Fleet().Get(ctx, orgId, fleetName)
		require.NoError(err)
		stopOverrides := decodeLifecycleOverrides(t, (*stoppedFleet.Metadata.Annotations)[domain.FleetAnnotationApplicationLifecycle])

		fleet, status := h.StartFleetApplication(ctx, orgId, fleetName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		startOverrides := decodeLifecycleOverrides(t, (*fleet.Metadata.Annotations)[domain.FleetAnnotationApplicationLifecycle])

		require.GreaterOrEqual(*startOverrides["app-1"].DesiredStateVersion, *stopOverrides["app-1"].DesiredStateVersion)
	})

	t.Run("Lifecycle calls for an unknown application return not found", func(t *testing.T) {
		require := require.New(t)
		h, orgId, fleetName := newLifecycleTestFleet(t, "app-1")

		_, status := h.StopFleetApplication(ctx, orgId, fleetName, "does-not-exist")
		require.Equal(int32(http.StatusNotFound), status.Code)
	})

	t.Run("Lifecycle calls for an unknown fleet return not found", func(t *testing.T) {
		require := require.New(t)
		h, orgId, _ := newLifecycleTestFleet(t, "app-1")

		_, status := h.StopFleetApplication(ctx, orgId, "does-not-exist", "app-1")
		require.Equal(int32(http.StatusNotFound), status.Code)
	})

	t.Run("Each lifecycle action emits a FleetKind ApplicationLifecycleChanged event", func(t *testing.T) {
		require := require.New(t)
		h, orgId, fleetName := newLifecycleTestFleet(t, "app-1")

		_, status := h.StopFleetApplication(ctx, orgId, fleetName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)

		list, err := h.store.Event().List(ctx, orgId, store.ListParams{})
		require.NoError(err)
		require.Len(list.Items, 1)
		require.Equal(domain.EventReasonApplicationLifecycleChanged, list.Items[0].Reason)
		require.Equal(domain.FleetKind, list.Items[0].InvolvedObject.Kind)
		require.Equal(fleetName, list.Items[0].InvolvedObject.Name)
	})
}
