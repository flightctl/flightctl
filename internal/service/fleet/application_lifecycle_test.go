package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/require"
)

// decodeLifecycleOverrides unmarshals a raw FleetAnnotationApplicationLifecycle annotation
// value for assertions that need to check individual fields (e.g. ignoring the exact,
// non-deterministic desiredStateVersion stamp).
func decodeLifecycleOverrides(t *testing.T, raw string) map[string]domain.ApplicationLifecycleOverride {
	t.Helper()
	overrides := map[string]domain.ApplicationLifecycleOverride{}
	require.NoError(t, json.Unmarshal([]byte(raw), &overrides))
	return overrides
}

// newLifecycleTestFleet creates a fleet whose device template declares a single container
// application named appName, registers it in a fresh ServiceHandler backed by the fake fleet
// store, and returns the handler along with the org and fleet name to use in calls.
func newLifecycleTestFleet(t *testing.T, appName string) (h *ServiceHandler, st *fakeFleetStore, ev *fakeEventsService, orgId uuid.UUID, fleetName string) {
	t.Helper()
	require := require.New(t)

	containerApp := domain.ContainerApplication{
		AppType: domain.AppTypeContainer,
		Name:    lo.ToPtr(appName),
	}
	require.NoError(containerApp.FromImageApplicationProviderSpec(domain.ImageApplicationProviderSpec{Image: "quay.io/test/app:v1"}))
	var app domain.ApplicationProviderSpec
	require.NoError(app.FromContainerApplication(containerApp))

	fleetName = "fleet-1"
	fleet := domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr(fleetName),
		},
	}
	fleet.Spec.Template.Spec.Applications = &[]domain.ApplicationProviderSpec{app}

	st = newFakeFleetStore()
	ev = &fakeEventsService{}
	h = NewServiceHandler(st, ev, nil)
	orgId = uuid.New()
	_, err := st.Create(context.Background(), orgId, &fleet, nil)
	require.NoError(err)

	return h, st, ev, orgId, fleetName
}

func TestStopStartFleetApplication(t *testing.T) {
	ctx := context.Background()

	t.Run("StopFleetApplication sets desiredState=stopped without touching the declarative template", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, fleetName := newLifecycleTestFleet(t, "app-1")

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
		h, _, _, orgId, fleetName := newLifecycleTestFleet(t, "app-1")

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
		h, st, _, orgId, fleetName := newLifecycleTestFleet(t, "app-1")

		_, status := h.StopFleetApplication(ctx, orgId, fleetName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		stoppedFleet, err := st.Get(ctx, orgId, fleetName)
		require.NoError(err)
		stopOverrides := decodeLifecycleOverrides(t, (*stoppedFleet.Metadata.Annotations)[domain.FleetAnnotationApplicationLifecycle])

		fleet, status := h.StartFleetApplication(ctx, orgId, fleetName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)
		startOverrides := decodeLifecycleOverrides(t, (*fleet.Metadata.Annotations)[domain.FleetAnnotationApplicationLifecycle])

		require.GreaterOrEqual(*startOverrides["app-1"].DesiredStateVersion, *stopOverrides["app-1"].DesiredStateVersion)
	})

	t.Run("Lifecycle calls for an unknown application return not found", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, fleetName := newLifecycleTestFleet(t, "app-1")

		_, status := h.StopFleetApplication(ctx, orgId, fleetName, "does-not-exist")
		require.Equal(int32(http.StatusNotFound), status.Code)
	})

	t.Run("Lifecycle calls for an unknown fleet return not found", func(t *testing.T) {
		require := require.New(t)
		h, _, _, orgId, _ := newLifecycleTestFleet(t, "app-1")

		_, status := h.StopFleetApplication(ctx, orgId, "does-not-exist", "app-1")
		require.Equal(int32(http.StatusNotFound), status.Code)
	})

	t.Run("Each lifecycle action emits a FleetKind ApplicationLifecycleChanged event", func(t *testing.T) {
		require := require.New(t)
		h, _, ev, orgId, fleetName := newLifecycleTestFleet(t, "app-1")

		_, status := h.StopFleetApplication(ctx, orgId, fleetName, "app-1")
		require.Equal(int32(http.StatusOK), status.Code)

		require.Len(ev.created, 1)
		require.Equal(domain.EventReasonApplicationLifecycleChanged, ev.created[0].Reason)
		require.Equal(domain.FleetKind, ev.created[0].InvolvedObject.Kind)
		require.Equal(fleetName, ev.created[0].InvolvedObject.Name)
	})
}
