package device_updater

import (
	"context"
	"fmt"
	"log"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "DeviceUpdater Suite")
}

func createDevices(numDevices int, ctx context.Context, deviceStore service.DeviceStoreInterface, orgId uuid.UUID) {
	for i := 1; i <= numDevices; i++ {
		resource := api.Device{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("mydevice-%d", i)),
				Labels: &map[string]string{"key": "value"},
			},
		}

		_, err := deviceStore.CreateDevice(ctx, orgId, &resource)
		if err != nil {
			log.Fatalf("creating device: %v", err)
		}
	}
}

func createFleet(ctx context.Context, fleetStore service.FleetStoreInterface, orgId uuid.UUID) *api.Fleet {
	resource := api.Fleet{
		Metadata: api.ObjectMeta{
			Name: util.StrToPtr("myfleet"),
		},
		Spec: api.FleetSpec{
			Selector: &api.LabelSelector{
				MatchLabels: map[string]string{"key": "value"},
			},
		},
	}

	fleet, err := fleetStore.CreateFleet(ctx, orgId, &resource)
	if err != nil {
		log.Fatalf("creating fleet: %v", err)
	}
	return fleet
}

var _ = Describe("DeviceUpdater", func() {
	var (
		log           *logrus.Logger
		ctx           context.Context
		orgId         uuid.UUID
		db            *gorm.DB
		deviceStore   service.DeviceStoreInterface
		fleetStore    service.FleetStoreInterface
		cfg           *config.Config
		dbName        string
		numDevices    int
		deviceUpdater *DeviceUpdater
		fleet         *api.Fleet
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numDevices = 3
		var stores *store.Store
		db, stores, cfg, dbName = store.PrepareDBForUnitTests(log)
		deviceStore = stores.GetDeviceStore()
		fleetStore = stores.GetFleetStore()
		deviceUpdater = NewDeviceUpdater(log, db, stores)

		var err error
		fleet = createFleet(ctx, fleetStore, orgId)
		fleet, err = fleetStore.GetFleet(ctx, orgId, *fleet.Metadata.Name)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, db, dbName)
	})

	Context("DeviceUpdater", func() {
		It("Update device specs good flow", func() {
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
			createDevices(numDevices, ctx, deviceStore, orgId)

			// First update
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my first OS"}
			_, _, err := fleetStore.CreateOrUpdateFleet(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			deviceUpdater.UpdateDevices()
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.GetDevice(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(*dev.Metadata.Generation).To(Equal(int64(2)))
				Expect(dev.Spec.Os.Image).To(Equal("my first OS"))
			}
			fleet, err = fleetStore.GetFleet(ctx, orgId, *fleet.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(2)))

			// Second update
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS"}
			_, _, err = fleetStore.CreateOrUpdateFleet(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			deviceUpdater.UpdateDevices()
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.GetDevice(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(*dev.Metadata.Generation).To(Equal(int64(3)))
				Expect(dev.Spec.Os.Image).To(Equal("my new OS"))
			}
			fleet, err = fleetStore.GetFleet(ctx, orgId, *fleet.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(3)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(3)))
		})

		It("Update device heartbeat conditions - no thresholds set", func() {
			createDevices(1, ctx, deviceStore, orgId)
			apiDev, err := deviceStore.GetDevice(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			apiDev.Status.UpdatedAt = util.StrToPtr(time.Now().Add(time.Hour * -10).Format(time.RFC3339))
			device := model.NewDeviceFromApiResource(apiDev)
			deviceUpdater.updateDeviceHeartbeatConditions(log, device, model.NewFleetFromApiResource(fleet))

			Expect(device.Status.Data.Conditions).ToNot(BeNil())
			Expect(len(*device.Status.Data.Conditions)).To(Equal(2))
			Expect((*device.Status.Data.Conditions)[0].Status).To(Equal(api.False))
			Expect(*(*device.Status.Data.Conditions)[0].Reason).To(Equal("No threshold set"))
			Expect((*device.Status.Data.Conditions)[1].Status).To(Equal(api.False))
			Expect(*(*device.Status.Data.Conditions)[1].Reason).To(Equal("No threshold set"))
		})

		It("Update device heartbeat conditions - thresholds set and OK", func() {
			createDevices(1, ctx, deviceStore, orgId)
			apiDev, err := deviceStore.GetDevice(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			apiDev.Status.UpdatedAt = util.StrToPtr(time.Now().Add(time.Hour * -10).Format(time.RFC3339))
			device := model.NewDeviceFromApiResource(apiDev)
			fleet.Spec.DeviceConditions.HeartbeatElapsedTimeWarning = util.StrToPtr("50h")
			fleet.Spec.DeviceConditions.HeartbeatElapsedTimeError = util.StrToPtr("50h")
			deviceUpdater.updateDeviceHeartbeatConditions(log, device, model.NewFleetFromApiResource(fleet))

			Expect(device.Status.Data.Conditions).ToNot(BeNil())
			Expect(len(*device.Status.Data.Conditions)).To(Equal(2))
			Expect((*device.Status.Data.Conditions)[0].Status).To(Equal(api.False))
			Expect(*(*device.Status.Data.Conditions)[0].Reason).To(Equal("Threshold not exceeded"))
			Expect((*device.Status.Data.Conditions)[1].Status).To(Equal(api.False))
			Expect(*(*device.Status.Data.Conditions)[1].Reason).To(Equal("Threshold not exceeded"))
		})

		It("Update device heartbeat conditions - thresholds set and not OK", func() {
			createDevices(1, ctx, deviceStore, orgId)
			apiDev, err := deviceStore.GetDevice(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			apiDev.Status.UpdatedAt = util.StrToPtr(time.Now().Add(time.Hour * -10).Format(time.RFC3339))
			device := model.NewDeviceFromApiResource(apiDev)
			fleet.Spec.DeviceConditions.HeartbeatElapsedTimeWarning = util.StrToPtr("1h")
			fleet.Spec.DeviceConditions.HeartbeatElapsedTimeError = util.StrToPtr("1h")
			deviceUpdater.updateDeviceHeartbeatConditions(log, device, model.NewFleetFromApiResource(fleet))

			Expect(device.Status.Data.Conditions).ToNot(BeNil())
			Expect(len(*device.Status.Data.Conditions)).To(Equal(2))
			Expect((*device.Status.Data.Conditions)[0].Status).To(Equal(api.True))
			Expect(*(*device.Status.Data.Conditions)[0].Reason).To(Equal("Threshold exceeded"))
			Expect(*(*device.Status.Data.Conditions)[0].Message).To(Equal("Threshold exceeded by 9h0m0s"))
			Expect((*device.Status.Data.Conditions)[1].Status).To(Equal(api.True))
			Expect(*(*device.Status.Data.Conditions)[1].Reason).To(Equal("Threshold exceeded"))
			Expect(*(*device.Status.Data.Conditions)[1].Message).To(Equal("Threshold exceeded by 9h0m0s"))
		})
	})
})
