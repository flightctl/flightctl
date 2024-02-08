package device_updater

import (
	"context"
	"fmt"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
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

var _ = Describe("DeviceUpdater", func() {
	var (
		log           *logrus.Logger
		ctx           context.Context
		orgId         uuid.UUID
		db            *gorm.DB
		deviceStore   service.DeviceStore
		fleetStore    service.FleetStore
		cfg           *config.Config
		dbName        string
		numDevices    int
		deviceUpdater *DeviceUpdater
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
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, db, dbName)
	})

	Context("DeviceUpdater", func() {
		It("Update devices good flow", func() {
			store.CreateTestFleets(1, ctx, fleetStore, orgId, "myfleet", true)
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
			store.CreateTestDevices(numDevices, ctx, deviceStore, orgId, true)

			// First update
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my first OS"}
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			deviceUpdater.UpdateDevices()
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(*dev.Metadata.Owner).To(Equal("fleet/myfleet-1"))
				Expect(*dev.Metadata.Generation).To(Equal(int64(2)))
				Expect(dev.Spec.Os.Image).To(Equal("my first OS"))
			}
			fleet, err = fleetStore.Get(ctx, orgId, *fleet.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(2)))

			// Second update
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS"}
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			deviceUpdater.UpdateDevices()
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(*dev.Metadata.Owner).To(Equal("fleet/myfleet-1"))
				Expect(*dev.Metadata.Generation).To(Equal(int64(3)))
				Expect(dev.Spec.Os.Image).To(Equal("my new OS"))
			}
			fleet, err = fleetStore.Get(ctx, orgId, *fleet.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(3)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(3)))
		})

		It("Update fleet owner good flow", func() {
			store.CreateTestDevices(numDevices, ctx, deviceStore, orgId, true)
			deviceUpdater.UpdateDevices()

			// No fleet, so nothing should have happened
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(dev.Metadata.Owner).To(BeNil())
			}

			// Create a fleet, the devices should be owned by the fleet
			store.CreateTestFleets(1, ctx, fleetStore, orgId, "myfleet", true)
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			deviceUpdater.UpdateDevices()
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(*dev.Metadata.Owner).To(Equal("fleet/myfleet-1"))
			}

			// Change the fleet selector, the devices should be ownerless
			fleet.Spec.Selector.MatchLabels = map[string]string{"something": "else"}
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			deviceUpdater.UpdateDevices()
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(*dev.Metadata.Owner).To(Equal(""))
			}

			// Create a new fleet, the devices should be owned by it
			store.CreateTestFleets(1, ctx, fleetStore, orgId, "myotherfleet", true)
			deviceUpdater.UpdateDevices()
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(*dev.Metadata.Owner).To(Equal("fleet/myotherfleet-1"))
			}

			// Update the selector for the first fleet so that we have a conflict
			fleet.Spec.Selector.MatchLabels = map[string]string{"key": "value"}
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			deviceUpdater.UpdateDevices()
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(*dev.Metadata.Owner).To(Equal("fleet/myotherfleet-1"))
			}
		})
	})
})
