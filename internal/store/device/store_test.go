package device

import (
	"testing"

	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
)

// TestNewDeviceStore_ReturnsNonNilStore proves the constructor builds a
// working Store without dialing the database. This also exercises the
// cross-package store.NewGenericStore[...] wiring, proving it compiles and
// runs at construction time with a nil DB handle.
func TestNewDeviceStore_ReturnsNonNilStore(t *testing.T) {
	req := require.New(t)

	s := NewDeviceStore(nil, logrus.New())

	req.NotNil(s)
}

func TestPreserveDeltaEstimatesFromSnapshot(t *testing.T) {
	storedSize := "12 KiB"
	callerSize := "99 MiB"
	storedEstimates := &model.DeviceDeltaEstimates{
		OS: &model.DeviceDeltaEstimate{DeltaSize: &storedSize},
	}
	callerEstimates := &model.DeviceDeltaEstimates{
		OS: &model.DeviceDeltaEstimate{DeltaSize: &callerSize},
	}

	tests := []struct {
		name     string
		existing *model.Device
		want     *model.DeviceDeltaEstimates
	}{
		{
			name: "When the snapshot has no service conditions it should clear caller-supplied estimates",
			want: nil,
		},
		{
			name: "When the snapshot has no estimates it should clear caller-supplied estimates",
			existing: &model.Device{
				ServiceConditions: model.MakeJSONField(model.ServiceConditions{}),
			},
			want: nil,
		},
		{
			name: "When the snapshot has estimates it should preserve them",
			existing: &model.Device{
				ServiceConditions: model.MakeJSONField(model.ServiceConditions{DeltaEstimates: storedEstimates}),
			},
			want: storedEstimates,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			updated := &model.Device{
				ServiceConditions: model.MakeJSONField(model.ServiceConditions{DeltaEstimates: callerEstimates}),
			}

			preserveDeltaEstimatesFromSnapshot(updated, tt.existing)

			require.NotNil(t, updated.ServiceConditions)
			require.Equal(t, tt.want, updated.ServiceConditions.Data.DeltaEstimates)
		})
	}
}
