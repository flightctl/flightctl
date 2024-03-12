package store_test

import (
	"context"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
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
		storeInst  store.Store
		devStore   store.Device
		cfg        *config.Config
		dbName     string
		numDevices int
		called     bool
		callback   store.DeviceStoreCallback
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numDevices = 3
		storeInst, cfg, dbName = store.PrepareDBForUnitTests(log)
		devStore = storeInst.Device()
		called = false
		callback = store.DeviceStoreCallback(func(before *model.Device, after *model.Device) { called = true })

		testutil.CreateTestDevices(3, ctx, devStore, orgId, nil, false)
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	Context("Device store", func() {
		It("Get device success", func() {
			dev, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Name).To(Equal("mydevice-1"))
		})

		It("Get device - not found error", func() {
			_, err := devStore.Get(ctx, orgId, "nonexistent")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Get device - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := devStore.Get(ctx, badOrgId, "mydevice-1")
			Expect(err).To(HaveOccurred())
			Expect(err).To(Equal(gorm.ErrRecordNotFound))
		})

		It("Delete device success", func() {
			err := devStore.Delete(ctx, orgId, "mydevice-1", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})

		It("Delete device success when not found", func() {
			err := devStore.Delete(ctx, orgId, "nonexistent", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeFalse())
		})

		It("Delete all devices in org", func() {
			called := false
			callback := store.DeviceStoreAllDeletedCallback(func(orgId uuid.UUID) {
				called = true
			})

			otherOrgId, _ := uuid.NewUUID()
			err := devStore.DeleteAll(ctx, otherOrgId, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			listParams := store.ListParams{Limit: 1000}
			devices, err := devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(devices.Items).To(HaveLen(numDevices))

			called = false
			err = devStore.DeleteAll(ctx, orgId, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			devices, err = devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(devices.Items).To(HaveLen(0))
		})

		It("List with paging", func() {
			listParams := store.ListParams{Limit: 1000}
			allDevices, err := devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(allDevices.Items).To(HaveLen(numDevices))
			allDevNames := make([]string, len(allDevices.Items))
			for i, dev := range allDevices.Items {
				allDevNames[i] = *dev.Metadata.Name
			}

			foundDevNames := make([]string, len(allDevices.Items))
			listParams.Limit = 1
			devices, err := devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))
			Expect(*devices.Metadata.RemainingItemCount).To(Equal(int64(2)))
			foundDevNames[0] = *devices.Items[0].Metadata.Name

			cont, err := store.ParseContinueString(devices.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			devices, err = devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))
			Expect(*devices.Metadata.RemainingItemCount).To(Equal(int64(1)))
			foundDevNames[1] = *devices.Items[0].Metadata.Name

			cont, err = store.ParseContinueString(devices.Metadata.Continue)
			Expect(err).ToNot(HaveOccurred())
			listParams.Continue = cont
			devices, err = devStore.List(ctx, orgId, listParams)
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
			listParams := store.ListParams{
				Limit:  1000,
				Labels: map[string]string{"key": "value-1"}}
			devices, err := devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))
			Expect(*devices.Items[0].Metadata.Name).To(Equal("mydevice-1"))
		})

		It("CreateOrUpdateDevice create mode", func() {
			templateVersion := "tv"
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("newresourcename"),
				},
				Spec: &api.DeviceSpec{
					TemplateVersion: &templateVersion,
				},
				Status: nil,
			}
			dev, created, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPI))
			Expect(dev.Kind).To(Equal(model.DeviceKind))
			Expect(*dev.Spec.TemplateVersion).To(Equal(templateVersion))
			Expect(dev.Status.Conditions).To(BeNil())
		})

		It("CreateOrUpdateDevice update mode templateVersion", func() {
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("mydevice-1"),
				},
				Spec: &api.DeviceSpec{
					TemplateVersion: util.StrToPtr("tv"),
				},
			}
			_, _, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, callback)
			Expect(err).To(HaveOccurred())
		})

		It("CreateOrUpdateDevice update mode", func() {
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("mydevice-1"),
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOSSpec{
						Image: "newos",
					},
				},
				Status: &api.DeviceStatus{
					Conditions: nil,
				},
			}
			dev, created, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPI))
			Expect(dev.Kind).To(Equal(model.DeviceKind))
			Expect(dev.Spec.Os.Image).To(Equal("newos"))
			Expect(dev.Status.Conditions).To(BeNil())
		})

		It("UpdateDeviceStatus", func() {
			templateVersion := "tv"
			// Random Condition to make sure Conditions do get stored
			condition := api.Condition{
				Type:               api.EnrollmentRequestApproved,
				LastTransitionTime: util.TimeStampStringPtr(),
				Status:             api.ConditionStatusFalse,
				Reason:             util.StrToPtr("reason"),
				Message:            util.StrToPtr("message"),
			}
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: util.StrToPtr("mydevice-1"),
				},
				Spec: &api.DeviceSpec{
					TemplateVersion: &templateVersion,
				},
				Status: &api.DeviceStatus{
					Conditions: &[]api.Condition{condition},
				},
			}
			_, err := devStore.UpdateStatus(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())
			dev, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPI))
			Expect(dev.Kind).To(Equal(model.DeviceKind))
			Expect(dev.Spec.TemplateVersion).To(BeNil())
			Expect(dev.Status.Conditions).ToNot(BeNil())
			Expect((*dev.Status.Conditions)[0].Type).To(Equal(api.EnrollmentRequestApproved))
		})

		It("UpdateTemplateVersionAndOwner", func() {
			called := false
			callback = store.DeviceStoreCallback(func(before *model.Device, after *model.Device) {
				called = true
			})

			dev, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())

			dev.Metadata.Owner = util.StrToPtr("newowner")
			dev.Spec.TemplateVersion = util.StrToPtr("tv")
			_, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, false, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			dev, err = devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Owner).ToNot(BeNil())
			Expect(*dev.Metadata.Owner).To(Equal("newowner"))
			Expect(dev.Spec.TemplateVersion).ToNot(BeNil())
			Expect(*dev.Spec.TemplateVersion).To(Equal("tv"))

			called = false
			dev.Metadata.Owner = nil
			dev.Spec.TemplateVersion = nil
			_, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, []string{"owner"}, false, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			dev, err = devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Owner).To(BeNil())
			Expect(dev.Spec.TemplateVersion).To(BeNil())
		})

		It("UpdateDeviceAnnotations", func() {
			firstAnnotations := map[string]string{"key1": "val1", "key2": "val2"}
			err := devStore.UpdateAnnotations(ctx, orgId, "mydevice-1", firstAnnotations, nil)
			Expect(err).ToNot(HaveOccurred())
			dev, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Annotations).ToNot(BeNil())
			Expect(*dev.Metadata.Annotations).To(HaveLen(2))
			Expect((*dev.Metadata.Annotations)["key1"]).To(Equal("val1"))

			err = devStore.UpdateAnnotations(ctx, orgId, "mydevice-1", map[string]string{"key1": "otherval"}, []string{"key2"})
			Expect(err).ToNot(HaveOccurred())
			dev, err = devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Annotations).ToNot(BeNil())
			Expect(*dev.Metadata.Annotations).To(HaveLen(1))
			Expect((*dev.Metadata.Annotations)["key1"]).To(Equal("otherval"))
			_, ok := (*dev.Metadata.Annotations)["key2"]
			Expect(ok).To(BeFalse())

			err = devStore.UpdateAnnotations(ctx, orgId, "mydevice-1", nil, []string{"key1"})
			Expect(err).ToNot(HaveOccurred())
			dev, err = devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(*dev.Metadata.Annotations).To(HaveLen(0))
		})

		It("GetRendered", func() {
			testutil.CreateTestFleet(ctx, storeInst.Fleet(), orgId, "fleet", nil, nil)
			err := testutil.CreateTestTemplateVersion(ctx, storeInst.TemplateVersion(), orgId, "fleet", "tv", "os", true)
			Expect(err).ToNot(HaveOccurred())
			testutil.CreateTestDevice(ctx, storeInst.Device(), orgId, "dev", util.SetResourceOwner(model.FleetKind, "fleet"), util.StrToPtr("tv"), nil)

			// Not passing owner and templateversion
			renderedConfig, err := devStore.GetRendered(ctx, orgId, "dev", nil, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(*renderedConfig.Config).To(Equal("rendered config"))
			Expect(renderedConfig.Os.Image).To(Equal("os"))

			// Passing wrong owner
			renderedConfig, err = devStore.GetRendered(ctx, orgId, "dev", util.StrToPtr("Fleet/otherfleet"), util.StrToPtr("tv"))
			Expect(err).ToNot(HaveOccurred())
			Expect(*renderedConfig.Config).To(Equal("rendered config"))
			Expect(renderedConfig.Os.Image).To(Equal("os"))

			// Passing wrong tv
			renderedConfig, err = devStore.GetRendered(ctx, orgId, "dev", util.StrToPtr("Fleet/fleet"), util.StrToPtr("othertv"))
			Expect(err).ToNot(HaveOccurred())
			Expect(*renderedConfig.Config).To(Equal("rendered config"))
			Expect(renderedConfig.Os.Image).To(Equal("os"))

			// Passing current owner and tv
			renderedConfig, err = devStore.GetRendered(ctx, orgId, "dev", util.StrToPtr("Fleet/fleet"), util.StrToPtr("tv"))
			Expect(err).ToNot(HaveOccurred())
			Expect(renderedConfig).To(BeNil())
		})
	})
})
