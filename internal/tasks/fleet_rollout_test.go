package tasks_test

import (
	"context"
	"fmt"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Tasks Suite")
}

var _ = Describe("FleetRollout", func() {
	var (
		log         *logrus.Logger
		ctx         context.Context
		orgId       uuid.UUID
		deviceStore store.Device
		fleetStore  store.Fleet
		storeInst   store.Store
		cfg         *config.Config
		dbName      string
		numDevices  int
		callback    store.FleetStoreCallback
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numDevices = 3
		storeInst, cfg, dbName = store.PrepareDBForUnitTests(log)
		deviceStore = storeInst.Device()
		fleetStore = storeInst.Fleet()
		callback = func(before *model.Fleet, after *model.Fleet) {}
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	Context("FleetRollout", func() {
		It("Fleet rollout good flow", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "myfleet-1", nil, nil)
			testutil.CreateTestDevices(numDevices, ctx, deviceStore, orgId, util.StrToPtr("Fleet/myfleet-1"), true)
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))

			// First update
			logic := tasks.NewFleetRolloutsLogic(log, storeInst, tasks.ResourceReference{OrgID: orgId, Name: *fleet.Metadata.Name})
			logic.SetItemsPerPage(2)

			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my first OS"}
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())
			err = logic.RolloutFleet(ctx)
			Expect(err).ToNot(HaveOccurred())
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(*dev.Metadata.Generation).To(Equal(int64(2)))
				Expect(dev.Spec.Os.Image).To(Equal("my first OS"))
			}
			fleet, err = fleetStore.Get(ctx, orgId, *fleet.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(2)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(2)))

			// Second update
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS"}
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())
			err = logic.RolloutFleet(ctx)
			Expect(err).ToNot(HaveOccurred())
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(*dev.Metadata.Generation).To(Equal(int64(3)))
				Expect(dev.Spec.Os.Image).To(Equal("my new OS"))
			}
			fleet, err = fleetStore.Get(ctx, orgId, *fleet.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(3)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(3)))
		})

		It("Device rollout good flow", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "myfleet-1", nil, nil)
			testutil.CreateTestDevices(1, ctx, deviceStore, orgId, util.StrToPtr("Fleet/myfleet-1"), true)
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))

			logic := tasks.NewFleetRolloutsLogic(log, storeInst, tasks.ResourceReference{OrgID: orgId, Name: "mydevice-1"})
			logic.SetItemsPerPage(2)

			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my first OS"}
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())
			err = logic.RolloutDevice(ctx)
			Expect(err).ToNot(HaveOccurred())
			dev, err := deviceStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Generation).To(Equal(int64(2)))
			Expect(dev.Spec.Os.Image).To(Equal("my first OS"))
		})
	})
})
