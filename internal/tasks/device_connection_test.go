package tasks

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/instrumentation/tracing"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/flightctl/flightctl/test/util"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/require"
	gomock "go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

// Note: Running this benchmark will require a database connection. You can use `make deploy` to deploy a database`
//
// go test  -v -benchmem -run=^$ -timeout 30m -bench ^BenchmarkDeviceConnectionPoll$ github.com/flightctl/flightctl/internal/tasks
func BenchmarkDeviceConnectionPoll(b *testing.B) {
	ctx := context.Background()
	log := log.InitLogs()
	s := util.InitTracerForTests()
	defer func() {
		if err := s(ctx); err != nil {
			b.Logf("Failed to shutdown tracer: %v", err)
		}
	}()

	ctx, span := tracing.StartSpan(ctx,
		"flightctl/tasks", "BenchmarkDeviceConnectionPoll")
	defer span.End()

	require := require.New(b)
	log.Level = logrus.ErrorLevel
	for _, deviceCount := range []int{1000, 2000, 5000} {
		dbStore, cfg, dbName, db := store.PrepareDBForUnitTests(ctx, log)

		ctrl := gomock.NewController(b)
		mockQueueProducer := queues.NewMockQueueProducer(ctrl)
		workerClient := worker_client.NewWorkerClient(mockQueueProducer, log)
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		require.NoError(err)
		serviceHandler := service.NewServiceHandler(dbStore, workerClient, kvStore, nil, log, "", "", []string{}, nil)

		devices := generateMockDevices(deviceCount)
		err = batchCreateDevices(ctx, db, devices, deviceCount)
		require.NoError(err)

		deviceNames := make([]string, deviceCount)
		for i := 0; i < deviceCount; i++ {
			deviceNames[i] = fmt.Sprintf("device-%d", i)
		}
		cleanupFn := func() {
			kvStore.Close()
			dbStore.Close()
			store.DeleteTestDB(ctx, log, cfg, dbStore, dbName)
		}
		b.Run(fmt.Sprintf("update_summary_status_%d_devices", deviceCount), func(b *testing.B) {
			err := benchmarkUpdateSummaryStatusBatch(ctx, b, log, db, serviceHandler, deviceNames)
			require.NoError(err)
		})
		cleanupFn()
	}
}

func benchmarkUpdateSummaryStatusBatch(ctx context.Context, b *testing.B, log *logrus.Logger, db *gorm.DB, serviceHandler service.Service, deviceNames []string) error {
	connection := NewDeviceConnection(log, serviceHandler)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StartTimer()
		connection.Poll(ctx, store.NullOrgId)
		b.StopTimer()

		err := resetDeviceStatus(ctx, db, deviceNames)
		if err != nil {
			return err
		}
	}
	return nil
}

func resetDeviceStatus(ctx context.Context, db *gorm.DB, deviceNames []string) error {
	status := domain.NewDeviceStatus()
	status.LastSeen = lo.ToPtr(time.Now().Add(-10 * time.Minute))
	status.Summary.Status = domain.DeviceSummaryStatusOnline
	err := db.WithContext(ctx).Transaction(func(innerTx *gorm.DB) (err error) {
		for _, name := range deviceNames {
			result := innerTx.Model(&model.Device{}).Where("name = ?", name).Update("status", status)
			if result.Error != nil {
				return store.ErrorFromGormError(result.Error)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to reset device status: %w", err)
	}
	return db.WithContext(ctx).Exec("VACUUM").Error
}

func generateMockDevices(count int) []domain.Device {
	devices := make([]domain.Device, count)
	status := domain.NewDeviceStatus()
	status.LastSeen = lo.ToPtr(time.Now().Add(-10 * time.Minute))
	status.Summary.Status = domain.DeviceSummaryStatusOnline
	for i := 0; i < count; i++ {
		devices[i] = domain.Device{
			Metadata: domain.ObjectMeta{
				Name: lo.ToPtr(fmt.Sprintf("device-%d", i)),
			},
			Spec: &domain.DeviceSpec{},

			Status: &status,
		}
	}
	return devices
}

func batchCreateDevices(ctx context.Context, db *gorm.DB, devices []domain.Device, batchSize int) error {
	for i := 0; i < len(devices); i += batchSize {
		end := i + batchSize
		if end > len(devices) {
			end = len(devices)
		}
		if err := batchCreateDeviceTransaction(ctx, db, devices[i:end]); err != nil {
			return fmt.Errorf("failed to insert batch: %w", err)
		}
	}

	return nil
}

func batchCreateDeviceTransaction(ctx context.Context, db *gorm.DB, devices []domain.Device) error {
	return db.WithContext(ctx).Transaction(func(innerTx *gorm.DB) (err error) {
		for _, device := range devices {
			deviceCopy := device
			modelDevice, err := model.NewDeviceFromApiResource(&deviceCopy)
			if err != nil {
				return flterrors.ErrIllegalResourceVersionFormat
			}
			result := innerTx.Create(modelDevice)
			if result.Error != nil {
				return store.ErrorFromGormError(result.Error)
			}
		}
		return nil
	})
}
