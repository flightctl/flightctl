package store_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

func TestFleetStore(t *testing.T) {
	require := require.New(t)
	ctx := context.Background()
	log := logrus.New()
	st, cfg, dbName, _ := store.PrepareDBForUnitTests(ctx, log)
	defer store.DeleteTestDB(ctx, log, cfg, st, dbName)

	fleetStore := st.Fleet()
	deviceStore := st.Device()
	orgId := uuid.New()

	fleet := &domain.Fleet{
		Metadata: domain.ObjectMeta{
			Name: lo.ToPtr("my-fleet"),
		},
		Spec: domain.FleetSpec{
			Selector: &domain.LabelSelector{
				MatchLabels: &map[string]string{"fleet": "my-fleet"},
			},
		},
	}
	_, err := fleetStore.Create(ctx, orgId, fleet, nil)
	require.NoError(err)

	device := &domain.Device{
		Metadata: domain.ObjectMeta{
			Name:   lo.ToPtr("my-device"),
			Labels: &map[string]string{"fleet": "my-fleet"},
			Owner:  lo.ToPtr(util.ResourceOwner(domain.FleetKind, "my-fleet")),
		},
	}
	_, err = deviceStore.Create(ctx, orgId, device, nil)
	require.NoError(err)

	t.Run("Get fleet with device summary", func(t *testing.T) {
		fleet, err := fleetStore.Get(ctx, orgId, "my-fleet", store.GetWithDeviceSummary(true))
		require.NoError(err)
		require.NotNil(fleet.Status)
		require.NotNil(fleet.Status.DevicesSummary)
		require.Equal(int64(1), fleet.Status.DevicesSummary.Total)
	})
}
