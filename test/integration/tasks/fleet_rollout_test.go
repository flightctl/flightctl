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
		tvStore     store.TemplateVersion
		storeInst   store.Store
		cfg         *config.Config
		dbName      string
		numDevices  int
		callback    store.FleetStoreCallback
		taskManager tasks.TaskManager
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numDevices = 3
		storeInst, cfg, dbName = store.PrepareDBForUnitTests(log)
		deviceStore = storeInst.Device()
		fleetStore = storeInst.Fleet()
		tvStore = storeInst.TemplateVersion()
		callback = func(before *model.Fleet, after *model.Fleet) {}
		taskManager = tasks.Init(log, storeInst)
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	Context("FleetRollout", func() {
		It("Fleet rollout good flow", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "myfleet-1", nil, nil)
			err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet-1", "1.0.bad", "my bad OS", false)
			Expect(err).ToNot(HaveOccurred())
			testutil.CreateTestDevices(numDevices, ctx, deviceStore, orgId, util.StrToPtr("Fleet/myfleet-1"), true)
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))

			devices, err := deviceStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(numDevices))

			// First update
			logic := tasks.NewFleetRolloutsLogic(taskManager, log, storeInst, tasks.ResourceReference{OrgID: orgId, Name: *fleet.Metadata.Name})
			logic.SetItemsPerPage(2)

			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet-1", "1.0.0", "my first OS", true)
			Expect(err).ToNot(HaveOccurred())
			err = logic.RolloutFleet(ctx)
			Expect(err).ToNot(HaveOccurred())
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(dev.Metadata.Annotations).ToNot(BeNil())
				Expect((*dev.Metadata.Annotations)[model.DeviceAnnotationTemplateVersion]).To(Equal("1.0.0"))
			}

			// Second update
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet-1", "1.0.1", "my new OS", true)
			Expect(err).ToNot(HaveOccurred())
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my new OS"}
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())
			err = logic.RolloutFleet(ctx)
			Expect(err).ToNot(HaveOccurred())
			for i := 1; i <= numDevices; i++ {
				dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
				Expect(err).ToNot(HaveOccurred())
				Expect(dev.Metadata.Annotations).ToNot(BeNil())
				Expect((*dev.Metadata.Annotations)[model.DeviceAnnotationTemplateVersion]).To(Equal("1.0.1"))
			}
		})

		It("Device rollout good flow", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, "myfleet-1", nil, nil)
			err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet-1", "1.0.bad", "my bad OS", false)
			Expect(err).ToNot(HaveOccurred())
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "mydevice-1", util.StrToPtr("Fleet/myfleet-1"), nil, nil)
			fleet, err := fleetStore.Get(ctx, orgId, "myfleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))

			logic := tasks.NewFleetRolloutsLogic(taskManager, log, storeInst, tasks.ResourceReference{OrgID: orgId, Name: "mydevice-1"})
			logic.SetItemsPerPage(2)

			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, "myfleet-1", "1.0.0", "my first OS", true)
			Expect(err).ToNot(HaveOccurred())
			err = logic.RolloutDevice(ctx)
			Expect(err).ToNot(HaveOccurred())
			dev, err := deviceStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Annotations).ToNot(BeNil())
			Expect((*dev.Metadata.Annotations)[model.DeviceAnnotationTemplateVersion]).To(Equal("1.0.0"))
		})
	})
})
