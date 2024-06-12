package store

import (
	"context"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/stretchr/testify/require"
)

// make deploy
// go test -v -benchmem -run=^$ -bench ^BenchmarkLabelSelector$ github.com/flightctl/flightctl/internal/store
func BenchmarkConditionSelector(b *testing.B) {
	require := require.New(b)
	log := log.InitLogs()
	store, _, _, err := PrepareDBForUnitTests(log)
	require.NoError(err)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	devices := GenerateMockDevices(100000)
	err = batchDevices(ctx, store, devices, 1000)
	require.NoError(err)

	conditions := make(map[string]string)
	conditions["Progressing"] = "True"
	conditions["Available"] = "True"
	conditions["Degraded"] = "True"

	orgID := NullOrgId

	listParams := ListParams{
		Conditions: conditions,
	}

	err = store.Maintenance()
	require.NoError(err)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		list, err := store.Device().List(ctx, orgID, listParams)
		require.NoError(err)
		require.NotNil(list)
		fmt.Printf("Listed %d devices\n", len(list.Items))
	}
}

// make deploy
// go test -v -benchmem -run=^$ -bench ^BenchmarkLabelSelector$ github.com/flightctl/flightctl/internal/store
func BenchmarkLabelSelector(b *testing.B) {
	require := require.New(b)
	log := log.InitLogs()
	store, _, _, err := PrepareDBForUnitTests(log)
	require.NoError(err)
	defer store.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	devices := GenerateMockDevices(100000)
	err = batchDevices(ctx, store, devices, 1000)
	require.NoError(err)

	labels := make(map[string]string)
	labels["app"] = "nginx"
	labels["env"] = "production"
	labels["tier"] = "backend"

	orgID := NullOrgId
	listParams := ListParams{
		Labels: labels,
	}

	err = store.Maintenance()
	require.NoError(err)

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		list, err := store.Device().List(ctx, orgID, listParams)
		require.NoError(err)
		require.NotNil(list)
		fmt.Printf("Listed %d devices\n", len(list.Items))
	}
}

func GenerateMockDevices(count int) []v1alpha1.Device {
	devices := make([]v1alpha1.Device, count)

	for i := 0; i < count; i++ {
		labels := GenerateMockLabels()
		conditions := GenerateMockConditions()
		devices[i] = v1alpha1.Device{
			Metadata: v1alpha1.ObjectMeta{
				Name: util.StrToPtr(fmt.Sprintf("device-%d", i)),
				Labels: &labels,
			},
			Spec: &v1alpha1.DeviceSpec{},

			Status: &v1alpha1.DeviceStatus{
				Conditions: &conditions,
			},
		}
	}
	return devices
}

func GenerateMockLabels() map[string]string {
	labels := make(map[string]string)
	labels["app"] = "nginx"
	labels["env"] = "production"
	labels["tier"] = "backend"

	return labels
}

func GenerateMockConditions() []v1alpha1.Condition {
	conditions := []v1alpha1.Condition{
		{
			Type: v1alpha1.DeviceProgressing,
		},
		{
			Type: v1alpha1.DeviceAvailable,
		},
		{
			Type: v1alpha1.DeviceDegraded,
		},
		{
			Type: v1alpha1.DeviceAppsAvailable,
		},
		{
			Type: v1alpha1.DeviceAppsDegraded,
		},
		{
			Type: v1alpha1.DeviceDiskPressure,
		},
		{
			Type: v1alpha1.DeviceMemoryPressure,
		},
		{
			Type: v1alpha1.DevicePIDPressure,
		},
		{
			Type: v1alpha1.DeviceMemoryPressure,
		},
		{
			Type: v1alpha1.DeviceCPUPressure,
		},
	}

	for i := range conditions {
		if rand.Intn(2) == 1 {
			conditions[i].Status = v1alpha1.ConditionStatusTrue
			conditions[i].Message = util.StrToPtr("All is well")
			conditions[i].Reason = util.StrToPtr("AsExpected")
		} else {
			conditions[i].Status = v1alpha1.ConditionStatusTrue // TODO: Change to False
			conditions[i].Message = util.StrToPtr("This is a longer string for the sake of generating reasonable data")
			conditions[i].Reason = util.StrToPtr("AnotherLongerReason")
		}
	}

	return conditions
}

func batchDevices(ctx context.Context, store Store, devices []v1alpha1.Device, batchSize int) error {
	for i := 0; i < len(devices); i += batchSize {
		end := i + batchSize
		if end > len(devices) {
			end = len(devices)
		}
		err := store.Device().Txn(ctx, devices[i:end])
		if err != nil {
			return err
		}
		fmt.Printf("Batch %d of %d devices inserted\n", end, len(devices))
	}
	return nil
}
