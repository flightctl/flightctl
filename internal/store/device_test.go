package store

import (
	"context"
	"fmt"
	"log"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Store Suite")
}

func createDevices(numDevices int, ctx context.Context, store *Store, orgId uuid.UUID) {
	for i := 1; i <= numDevices; i++ {
		resource := api.Device{
			Metadata: api.ObjectMeta{
				Name:   util.StrToPtr(fmt.Sprintf("mydevice-%d", i)),
				Labels: &map[string]string{"key": fmt.Sprintf("value-%d", i)},
			},
			Spec: api.DeviceSpec{
				Os: &api.DeviceOSSpec{
					Image: "myimage",
				},
			},
		}

		_, err := store.deviceStore.CreateDevice(ctx, orgId, &resource)
		if err != nil {
			log.Fatalf("creating device: %v", err)
		}
	}
}

var _ = Describe("DeviceStore create", func() {
	var (
		log        *logrus.Logger
		ctx        context.Context
		orgId      uuid.UUID
		db         *gorm.DB
		store      *Store
		cfg        *config.Config
		dbName     string
		numDevices int
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numDevices = 3
		db, store, cfg, dbName = PrepareDBForUnitTests(log)

		createDevices(3, ctx, store, orgId)
	})

	AfterEach(func() {
		DeleteTestDB(cfg, db, dbName)
	})

	Context("Device store", func() {
		It("Get device success", func() {
			dev, err := store.deviceStore.GetDevice(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Name).To(Equal("mydevice-1"))
		})

		It("Get device - not found error", func() {
			_, err := store.deviceStore.GetDevice(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Get device - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := store.deviceStore.GetDevice(ctx, badOrgId, "mydevice-1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Delete device success", func() {
			err := store.deviceStore.DeleteDevice(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete device success when not found", func() {
			err := store.deviceStore.DeleteDevice(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete all devices in org", func() {
			otherOrgId, _ := uuid.NewUUID()
			err := store.deviceStore.DeleteDevices(ctx, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			listParams := service.ListParams{Limit: 1000}
			devices, err := store.deviceStore.ListDevices(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(numDevices))

			err = store.deviceStore.DeleteDevices(ctx, orgId)
			Expect(err).ToNot(HaveOccurred())

			devices, err = store.deviceStore.ListDevices(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(0))
		})

		It("List with paging", func() {
			listParams := service.ListParams{Limit: 1000}
			allDevices, err := store.deviceStore.ListDevices(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allDevices.Items)).To(Equal(numDevices))
			allDevNames := make([]string, len(allDevices.Items))
			for i, dev := range allDevices.Items {
				allDevNames[i] = *dev.Metadata.Name
			}

			foundDevNames := make([]string, len(allDevices.Items))
			listParams.Limit = 1
			devices, err := store.deviceStore.ListDevices(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))
			Expect(*devices.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundDevNames[0] = *devices.Items[0].Metadata.Name

			cont, err := service.ParseContinueString(devices.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			devices, err = store.deviceStore.ListDevices(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))
			Expect(*devices.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundDevNames[1] = *devices.Items[0].Metadata.Name

			cont, err = service.ParseContinueString(devices.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			devices, err = store.deviceStore.ListDevices(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))
			Expect(devices.Metadata.RemainingItemCount).To(BeNil())
			Expect(devices.Metadata.Continue).To(BeNil())
			foundDevNames[2] = *devices.Items[0].Metadata.Name

			for i := range allDevNames {
				Expect(allDevNames[i]).To(Equal(foundDevNames[i]))
			}
		})

		It("List with paging", func() {
			listParams := service.ListParams{
				Limit:  1000,
				Labels: map[string]string{"key": "value-1"}}
			devices, err := store.deviceStore.ListDevices(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))
			Expect(*devices.Items[0].Metadata.Name).To(Equal("mydevice-1"))
		})

		It("CreateOrUpdateDevice create mode", func() {
			imageUrl := "imageurl"
			condition := api.DeviceCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("newresourcename"),
				},
				Spec: api.DeviceSpec{
					Os: &api.DeviceOSSpec{
						Image: imageUrl,
					},
				},
				Status: &api.DeviceStatus{
					Conditions: &[]api.DeviceCondition{condition},
				},
			}
			dev, created, err := store.deviceStore.CreateOrUpdateDevice(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPI))
			Expect(dev.Kind).To(Equal(model.DeviceKind))
			Expect(dev.Spec.Os.Image).To(Equal(imageUrl))
			Expect(dev.Status.Conditions).To(BeNil())
		})

		It("CreateOrUpdateDevice update mode", func() {
			imageUrl := "imageurl"
			condition := api.DeviceCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("mydevice-1"),
				},
				Spec: api.DeviceSpec{
					Os: &api.DeviceOSSpec{
						Image: imageUrl,
					},
				},
				Status: &api.DeviceStatus{
					Conditions: &[]api.DeviceCondition{condition},
				},
			}
			dev, created, err := store.deviceStore.CreateOrUpdateDevice(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPI))
			Expect(dev.Kind).To(Equal(model.DeviceKind))
			Expect(dev.Spec.Os.Image).To(Equal(imageUrl))
			Expect(dev.Status.Conditions).To(BeNil())
		})

		It("UpdateDeviceStatus", func() {
			imageUrl := "imageurl"
			condition := api.DeviceCondition{
				Type:               "type",
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.False,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("mydevice-1"),
				},
				Spec: api.DeviceSpec{
					Os: &api.DeviceOSSpec{
						Image: imageUrl,
					},
				},
				Status: &api.DeviceStatus{
					Conditions: &[]api.DeviceCondition{condition},
				},
			}
			_, err := store.deviceStore.UpdateDeviceStatus(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())
			dev, err := store.deviceStore.GetDevice(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPI))
			Expect(dev.Kind).To(Equal(model.DeviceKind))
			Expect(dev.Spec.Os.Image).ToNot(Equal(imageUrl))
			Expect(dev.Status.Conditions).ToNot(BeNil())
			Expect((*dev.Status.Conditions)[0].Type).To(Equal("type"))
		})
	})
})
