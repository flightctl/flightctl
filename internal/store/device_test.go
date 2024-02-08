package store

import (
	"context"
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

		CreateTestDevices(3, ctx, store.deviceStore, orgId, false)
	})

	AfterEach(func() {
		DeleteTestDB(cfg, db, dbName)
	})

	Context("Device store", func() {
		It("Get device success", func() {
			dev, err := store.deviceStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Name).To(Equal("mydevice-1"))
		})

		It("Get device - not found error", func() {
			_, err := store.deviceStore.Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Get device - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := store.deviceStore.Get(ctx, badOrgId, "mydevice-1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Delete device success", func() {
			err := store.deviceStore.Delete(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete device success when not found", func() {
			err := store.deviceStore.Delete(ctx, orgId, "nonexistent")
			Expect(err).ToNot(HaveOccurred())
		})

		It("Delete all devices in org", func() {
			otherOrgId, _ := uuid.NewUUID()
			err := store.deviceStore.DeleteAll(ctx, otherOrgId)
			Expect(err).ToNot(HaveOccurred())

			listParams := service.ListParams{Limit: 1000}
			devices, err := store.deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(numDevices))

			err = store.deviceStore.DeleteAll(ctx, orgId)
			Expect(err).ToNot(HaveOccurred())

			devices, err = store.deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(0))
		})

		It("List with paging", func() {
			listParams := service.ListParams{Limit: 1000}
			allDevices, err := store.deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(allDevices.Items)).To(Equal(numDevices))
			allDevNames := make([]string, len(allDevices.Items))
			for i, dev := range allDevices.Items {
				allDevNames[i] = *dev.Metadata.Name
			}

			foundDevNames := make([]string, len(allDevices.Items))
			listParams.Limit = 1
			devices, err := store.deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))
			Expect(*devices.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundDevNames[0] = *devices.Items[0].Metadata.Name

			cont, err := service.ParseContinueString(devices.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			devices, err = store.deviceStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))
			Expect(*devices.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundDevNames[1] = *devices.Items[0].Metadata.Name

			cont, err = service.ParseContinueString(devices.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			devices, err = store.deviceStore.List(ctx, orgId, listParams)
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
			devices, err := store.deviceStore.List(ctx, orgId, listParams)
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
			dev, created, err := store.deviceStore.CreateOrUpdate(ctx, orgId, &device)
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
			dev, created, err := store.deviceStore.CreateOrUpdate(ctx, orgId, &device)
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
			_, err := store.deviceStore.UpdateStatus(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())
			dev, err := store.deviceStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPI))
			Expect(dev.Kind).To(Equal(model.DeviceKind))
			Expect(dev.Spec.Os.Image).ToNot(Equal(imageUrl))
			Expect(dev.Status.Conditions).ToNot(BeNil())
			Expect((*dev.Status.Conditions)[0].Type).To(Equal("type"))
		})
	})
})
