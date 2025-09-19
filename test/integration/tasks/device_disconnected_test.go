package tasks_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

var _ = Describe("DeviceDisconnected", func() {
	var (
		log              *logrus.Logger
		ctx              context.Context
		orgId            uuid.UUID
		deviceStore      store.Device
		storeInst        store.Store
		serviceHandler   service.Service
		cfg              *config.Config
		dbName           string
		workerClient     *worker_client.MockWorkerClient
		ctrl             *gomock.Controller
		disconnectedTask *tasks.DeviceDisconnected
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		deviceStore = storeInst.Device()
		ctrl = gomock.NewController(GinkgoT())
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		workerClient = worker_client.NewMockWorkerClient(ctrl)
		Expect(err).ToNot(HaveOccurred())
		orgResolver := testutil.NewOrgResolver(cfg, storeInst.Organization(), log)
		serviceHandler = service.NewServiceHandler(storeInst, workerClient, kvStore, nil, log, "", "", []string{}, orgResolver)
		disconnectedTask = tasks.NewDeviceDisconnected(log, serviceHandler)
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
		ctrl.Finish()
	})

	Context("when there are no devices", func() {
		It("should complete successfully without errors", func() {
			disconnectedTask.Poll(ctx)
			// Should not panic or error
		})
	})

	Context("when devices are connected", func() {
		BeforeEach(func() {
			// Create devices that have checked in recently
			for i := 1; i <= 3; i++ {
				deviceName := fmt.Sprintf("connected-device-%d", i)
				testutil.CreateTestDevice(ctx, deviceStore, orgId, deviceName, nil, nil, nil)

				// Get the device and update its status
				device, err := deviceStore.Get(ctx, orgId, deviceName)
				Expect(err).ToNot(HaveOccurred())

				// Set a recent last seen time
				device.Status.LastSeen = lo.ToPtr(time.Now().Add(-1 * time.Minute))
				device.Status.Summary.Status = api.DeviceSummaryStatusOnline
				_, err = deviceStore.UpdateStatus(ctx, orgId, device, nil)
				Expect(err).ToNot(HaveOccurred())
			}
		})

		It("should not mark connected devices as unknown", func() {
			disconnectedTask.Poll(ctx)

			// Check that all devices remain online
			for i := 1; i <= 3; i++ {
				deviceName := fmt.Sprintf("connected-device-%d", i)
				device, err := deviceStore.Get(ctx, orgId, deviceName)
				Expect(err).ToNot(HaveOccurred())
				Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))
			}
		})
	})

	Context("when devices are disconnected", func() {
		BeforeEach(func() {
			// Create devices that haven't checked in for a while
			for i := 1; i <= 3; i++ {
				deviceName := fmt.Sprintf("disconnected-device-%d", i)
				testutil.CreateTestDevice(ctx, deviceStore, orgId, deviceName, nil, nil, nil)

				// Get the device and update its status
				device, err := deviceStore.Get(ctx, orgId, deviceName)
				Expect(err).ToNot(HaveOccurred())

				// Set an old last seen time (more than DeviceDisconnectedTimeout ago)
				device.Status.LastSeen = lo.ToPtr(time.Now().Add(-10 * time.Minute))
				device.Status.Summary.Status = api.DeviceSummaryStatusOnline
				device.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
				device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
				_, err = deviceStore.UpdateStatus(ctx, orgId, device, nil)
				Expect(err).ToNot(HaveOccurred())
			}
		})

		It("should mark disconnected devices as unknown", func() {
			workerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any()).Times(3)
			disconnectedTask.Poll(ctx)

			// Check that all devices are marked as unknown
			for i := 1; i <= 3; i++ {
				deviceName := fmt.Sprintf("disconnected-device-%d", i)
				device, err := deviceStore.Get(ctx, orgId, deviceName)
				Expect(err).ToNot(HaveOccurred())
				Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusUnknown))
				Expect(device.Status.Summary.Info).ToNot(BeNil())
				Expect(*device.Status.Summary.Info).To(ContainSubstring("Device is disconnected"))
				Expect(device.Status.ApplicationsSummary.Status).To(Equal(api.ApplicationsSummaryStatusUnknown))
			}
		})
	})

	Context("when there are mixed connected and disconnected devices", func() {
		BeforeEach(func() {
			// Create connected devices
			for i := 1; i <= 2; i++ {
				deviceName := fmt.Sprintf("connected-%d", i)
				testutil.CreateTestDevice(ctx, deviceStore, orgId, deviceName, nil, nil, nil)

				// Get the device and update its status
				device, err := deviceStore.Get(ctx, orgId, deviceName)
				Expect(err).ToNot(HaveOccurred())

				device.Status.LastSeen = lo.ToPtr(time.Now().Add(-1 * time.Minute))
				device.Status.Summary.Status = api.DeviceSummaryStatusOnline
				device.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
				device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
				_, err = deviceStore.UpdateStatus(ctx, orgId, device, nil)
				Expect(err).ToNot(HaveOccurred())
			}

			// Create disconnected devices
			for i := 1; i <= 3; i++ {
				deviceName := fmt.Sprintf("disconnected-%d", i)
				testutil.CreateTestDevice(ctx, deviceStore, orgId, deviceName, nil, nil, nil)

				// Get the device and update its status
				device, err := deviceStore.Get(ctx, orgId, deviceName)
				Expect(err).ToNot(HaveOccurred())

				device.Status.LastSeen = lo.ToPtr(time.Now().Add(-10 * time.Minute))
				device.Status.Summary.Status = api.DeviceSummaryStatusOnline
				device.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
				device.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
				_, err = deviceStore.UpdateStatus(ctx, orgId, device, nil)
				Expect(err).ToNot(HaveOccurred())
			}
		})

		It("should only mark disconnected devices as unknown", func() {
			workerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any()).Times(3)
			disconnectedTask.Poll(ctx)

			// Check connected devices remain online
			for i := 1; i <= 2; i++ {
				deviceName := fmt.Sprintf("connected-%d", i)
				device, err := deviceStore.Get(ctx, orgId, deviceName)
				Expect(err).ToNot(HaveOccurred())
				Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))
				Expect(device.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusUpToDate))
				Expect(device.Status.ApplicationsSummary.Status).To(Equal(api.ApplicationsSummaryStatusHealthy))
			}

			// Check disconnected devices are marked as unknown
			for i := 1; i <= 3; i++ {
				deviceName := fmt.Sprintf("disconnected-%d", i)
				device, err := deviceStore.Get(ctx, orgId, deviceName)
				Expect(err).ToNot(HaveOccurred())
				Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusUnknown))
				Expect(device.Status.ApplicationsSummary.Status).To(Equal(api.ApplicationsSummaryStatusUnknown))
			}
		})
	})

	Context("with edge case timing", func() {
		BeforeEach(func() {
			// Create a device that's right at the disconnection threshold
			deviceName := "threshold-device"
			testutil.CreateTestDevice(ctx, deviceStore, orgId, deviceName, nil, nil, nil)

			// Get the device and update its status
			device, err := deviceStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())

			// Set last seen to exactly the disconnection timeout
			device.Status.LastSeen = lo.ToPtr(time.Now().Add(-api.DeviceDisconnectedTimeout))
			device.Status.Summary.Status = api.DeviceSummaryStatusOnline
			_, err = deviceStore.UpdateStatus(ctx, orgId, device, nil)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should handle devices at the disconnection threshold correctly", func() {
			workerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
			disconnectedTask.Poll(ctx)

			device, err := deviceStore.Get(ctx, orgId, "threshold-device")
			Expect(err).ToNot(HaveOccurred())
			// Due to timing precision, this could go either way, but should not error
			Expect(device.Status.Summary.Status).To(BeElementOf(api.DeviceSummaryStatusOnline, api.DeviceSummaryStatusUnknown))
		})
	})

	Context("when field selector is used for optimization", func() {
		BeforeEach(func() {
			// Create a mix of devices with different last seen times
			// Recent device
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "recent-device", nil, nil, nil)
			recentDevice, err := deviceStore.Get(ctx, orgId, "recent-device")
			Expect(err).ToNot(HaveOccurred())
			recentDevice.Status.LastSeen = lo.ToPtr(time.Now().Add(-1 * time.Minute))
			recentDevice.Status.Summary.Status = api.DeviceSummaryStatusOnline
			recentDevice.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
			recentDevice.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
			_, err = deviceStore.UpdateStatus(ctx, orgId, recentDevice, nil)
			Expect(err).ToNot(HaveOccurred())

			// Old device
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "old-device", nil, nil, nil)
			oldDevice, err := deviceStore.Get(ctx, orgId, "old-device")
			Expect(err).ToNot(HaveOccurred())
			oldDevice.Status.LastSeen = lo.ToPtr(time.Now().Add(-10 * time.Minute))
			oldDevice.Status.Summary.Status = api.DeviceSummaryStatusOnline
			oldDevice.Status.Updated.Status = api.DeviceUpdatedStatusUpToDate
			oldDevice.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusHealthy
			_, err = deviceStore.UpdateStatus(ctx, orgId, oldDevice, nil)
			Expect(err).ToNot(HaveOccurred())
		})

		It("should use field selector to efficiently query only disconnected devices", func() {
			workerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any()).Times(1)
			disconnectedTask.Poll(ctx)

			// Recent device should remain online
			recentDevice, err := deviceStore.Get(ctx, orgId, "recent-device")
			Expect(err).ToNot(HaveOccurred())
			Expect(recentDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))
			Expect(recentDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusUpToDate))
			Expect(recentDevice.Status.ApplicationsSummary.Status).To(Equal(api.ApplicationsSummaryStatusHealthy))

			// Old device should be marked as unknown
			oldDevice, err := deviceStore.Get(ctx, orgId, "old-device")
			Expect(err).ToNot(HaveOccurred())
			Expect(oldDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusUnknown))
			Expect(oldDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusUnknown))
			Expect(oldDevice.Status.ApplicationsSummary.Status).To(Equal(api.ApplicationsSummaryStatusUnknown))
		})
	})
})
