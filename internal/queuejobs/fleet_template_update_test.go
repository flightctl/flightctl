package queuejobs

import (
	"context"
	"fmt"
	"log"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/riverqueue/river"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func createDevices(numDevices int, ctx context.Context, deviceStore store.Device, orgId uuid.UUID) {
	for i := 1; i <= numDevices; i++ {
		resource := api.Device{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("mydevice-%d", i)),
				Labels: &map[string]string{"key": "value"},
			},
		}

		_, err := deviceStore.Create(ctx, orgId, &resource)
		if err != nil {
			log.Fatalf("creating device: %v", err)
		}
	}
}

func createFleet(ctx context.Context, fleetStore store.Fleet, orgId uuid.UUID) *api.Fleet {
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

	fleet, err := fleetStore.Create(ctx, orgId, &resource)
	if err != nil {
		log.Fatalf("creating fleet: %v", err)
	}
	return fleet
}

var _ = Describe("DeviceUpdater", func() {
	var (
		log                       *logrus.Logger
		ctx                       context.Context
		orgId                     uuid.UUID
		db                        *gorm.DB
		deviceStore               store.Device
		fleetStore                store.Fleet
		cfg                       *config.Config
		dbName                    string
		numDevices                int
		fleetTemplateUpdateWorker *FleetTemplateUpdateWorker
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numDevices = 3
		var stores store.Store
		db, stores, cfg, dbName = store.PrepareDBForUnitTests(log)
		deviceStore = stores.Device()
		fleetStore = stores.Fleet()
		fleetTemplateUpdateWorker = NewFleetTemplateUpdateWorker(log, db, stores)
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, db, dbName)
	})

	Context("DeviceUpdater", func() {
		It("Update devices good flow", func() {
			Skip("Need to mock riverClient first")
			fleet := createFleet(ctx, fleetStore, orgId)
			fleet, err := fleetStore.Get(ctx, orgId, *fleet.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))
			createDevices(numDevices, ctx, deviceStore, orgId)
			job := river.Job[store.FleetTemplateUpdateArgs]{Args: store.FleetTemplateUpdateArgs{OrgID: orgId.String(), Name: *fleet.Metadata.Name}}

			// First update
			fleet.Spec.Template.Spec.Os = &api.DeviceOSSpec{Image: "my first OS"}
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			err = fleetTemplateUpdateWorker.Work(ctx, &job)
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
			_, _, err = fleetStore.CreateOrUpdate(ctx, orgId, fleet)
			Expect(err).ToNot(HaveOccurred())
			err = fleetTemplateUpdateWorker.Work(ctx, &job)
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
	})
})
