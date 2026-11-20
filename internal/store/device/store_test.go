package device

import (
	"testing"

	"github.com/flightctl/flightctl/internal/store"
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

func TestNormalizeSortColumns(t *testing.T) {
	tests := []struct {
		name     string
		columns  []store.SortColumn
		expected []store.SortColumn
	}{
		{name: "When no columns are set it should leave the default to the query", columns: nil, expected: nil},
		{name: "When sorting by name it should keep name", columns: []store.SortColumn{store.SortByName}, expected: []store.SortColumn{store.SortByName}},
		{name: "When sorting by alias only it should add name as a tiebreaker", columns: []store.SortColumn{store.SortByAlias}, expected: []store.SortColumn{store.SortByAlias, store.SortByName}},
		{name: "When sorting by alias and name it should keep both", columns: []store.SortColumn{store.SortByAlias, store.SortByName}, expected: []store.SortColumn{store.SortByAlias, store.SortByName}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.expected, NormalizeSortColumns(tt.columns))
		})
	}
}
