package device

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderedGenerationGuard(t *testing.T) {
	for _, changeDuringWrite := range []bool{false, true} {
		name := "When an unchanged render matches the device generation it should succeed"
		if changeDuringWrite {
			name = "When the device changes after rendering it should retain newer output"
		}
		t.Run(name, func(t *testing.T) {
			st, _, svc := newTestHandler()
			orgID := uuid.New()
			holdTestDevice(t, st, orgID, nil, nil, nil)
			st.device.devices["dev"].Metadata.Generation = lo.ToPtr(int64(7))
			if changeDuringWrite {
				st.device.beforeMutate = func(int) { st.device.devices["dev"].Metadata.Generation = lo.ToPtr(int64(8)) }
			}
			status := svc.UpdateRenderedDevice(context.Background(), orgID, "dev", "new-config", "new-apps", "hash", "os:2", nil, false, nil, RenderedDeviceOptions{ExpectedGeneration: lo.ToPtr(int64(7))})
			if changeDuringWrite {
				require.EqualValues(t, http.StatusConflict, status.Code)
				assert.Equal(t, "previous-config", st.device.rendered["dev"].Config)
				assert.Equal(t, "1", (*st.device.devices["dev"].Metadata.Annotations)[domain.DeviceAnnotationRenderedVersion])
			} else {
				require.EqualValues(t, http.StatusOK, status.Code, status.Message)
				assert.Equal(t, "previous-config", st.device.rendered["dev"].Config)
			}
		})
	}
}

func TestFleetSpecAndReferences(t *testing.T) {
	for _, moved := range []bool{false, true} {
		name := "When reference replacement fails it should keep the previous rollout marker"
		if moved {
			name = "When ownership changes before rollout commits it should retain the new fleet references"
		}
		t.Run(name, func(t *testing.T) {
			st, _, svc := newTestHandler()
			orgID := uuid.New()
			owner := "Fleet/old"
			holdTestDevice(t, st, orgID, &owner, nil, nil)
			st.device.fleetRefs = map[string][]model.DependencyRef{"dev": {{ResourceKey: "previous"}}}
			if moved {
				st.device.beforeMutate = func(int) {
					st.device.devices["dev"].Metadata.Owner = lo.ToPtr("Fleet/new")
					st.device.fleetRefs["dev"] = []model.DependencyRef{{ResourceKey: "new-fleet"}}
				}
			} else {
				st.device.fleetRefsWriteErr = errors.New("injected reference failure")
			}
			refs := []model.DependencyRef{{ResourceKey: "target"}}
			_, status := svc.ReplaceDeviceSpec(context.Background(), orgID, "dev", &owner, domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "os:2"}}, map[string]string{domain.DeviceAnnotationTemplateVersion: "target"}, nil, SpecUpdateOptions{FleetDependencyRefs: &refs})
			require.NotEqualValues(t, http.StatusOK, status.Code)
			assert.Equal(t, "os:1", st.device.devices["dev"].Spec.Os.Image)
			assert.NotContains(t, *st.device.devices["dev"].Metadata.Annotations, domain.DeviceAnnotationTemplateVersion)
			if moved {
				assert.Equal(t, "new-fleet", st.device.fleetRefs["dev"][0].ResourceKey)
			} else {
				assert.Equal(t, "previous", st.device.fleetRefs["dev"][0].ResourceKey)
				st.device.fleetRefsWriteErr = nil
				_, status = svc.ReplaceDeviceSpec(context.Background(), orgID, "dev", &owner, domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "os:2"}}, map[string]string{domain.DeviceAnnotationTemplateVersion: "target"}, nil, SpecUpdateOptions{FleetDependencyRefs: &refs})
				require.EqualValues(t, http.StatusOK, status.Code, status.Message)
				assert.Equal(t, "target", st.device.fleetRefs["dev"][0].ResourceKey)
			}
		})
	}
}

func TestFakeTransactionRestoresRenderedAndLastSeen(t *testing.T) {
	st, _, _ := newTestHandler()
	orgID := uuid.New()
	holdTestDevice(t, st, orgID, nil, nil, nil)
	before := time.Now().UTC()
	st.device.lastSeen["dev"] = &before
	err := st.device.WithTransaction(context.Background(), func(ctx context.Context) error {
		_, _, _, err := st.device.Mutate(ctx, orgID, "dev", nil, func(m *devicestore.DeviceMutation) error {
			m.Rendered = &devicestore.DeviceRendered{Config: "uncommitted"}
			m.Device.Status.LastSeen = lo.ToPtr(before.Add(time.Hour))
			return nil
		})
		require.NoError(t, err)
		return errors.New("rollback")
	})
	require.Error(t, err)
	assert.Equal(t, "previous-config", st.device.rendered["dev"].Config)
	assert.Equal(t, before, *st.device.lastSeen["dev"])
}
