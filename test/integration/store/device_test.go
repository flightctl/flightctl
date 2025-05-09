package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/flightctl/flightctl/api/v1alpha1"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/store/selector"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Store Suite")
}

var _ = Describe("DeviceStore create", func() {
	var (
		log                *logrus.Logger
		ctx                context.Context
		orgId              uuid.UUID
		storeInst          store.Store
		devStore           store.Device
		cfg                *config.Config
		db                 *gorm.DB
		dbName             string
		numDevices         int
		called             bool
		callback           store.DeviceStoreCallback
		allDeletedCallback store.DeviceStoreAllDeletedCallback
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numDevices = 3
		storeInst, cfg, dbName, db = store.PrepareDBForUnitTests(log)
		devStore = storeInst.Device()
		called = false
		callback = store.DeviceStoreCallback(func(uuid.UUID, *api.Device, *api.Device) { called = true })
		allDeletedCallback = store.DeviceStoreAllDeletedCallback(func(orgId uuid.UUID) { called = true })

		testutil.CreateTestDevices(ctx, 3, devStore, orgId, nil, false)
	})

	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
	})

	It("CreateOrUpdateDevice create mode race", func() {
		imageName := "tv"
		device := api.Device{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("newresourcename"),
			},
			Spec: &api.DeviceSpec{
				Os: &api.DeviceOsSpec{Image: imageName},
			},
			Status: nil,
		}

		raceCalled := false
		race := func() {
			if raceCalled {
				return
			}
			raceCalled = true
			result := db.Create(&model.Device{Resource: model.Resource{OrgID: orgId, Name: "newresourcename", ResourceVersion: lo.ToPtr(int64(1))}, Spec: model.MakeJSONField(api.DeviceSpec{})})
			Expect(result.Error).ToNot(HaveOccurred())
		}
		devStore.SetIntegrationTestCreateOrUpdateCallback(race)

		_, created, _, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, nil, callback)
		Expect(err).ToNot(HaveOccurred())
		Expect(created).To(BeFalse())
	})

	It("CreateOrUpdateDevice update mode race", func() {
		status := api.NewDeviceStatus()
		device := api.Device{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("mydevice-1"),
			},
			Spec: &api.DeviceSpec{
				Os: &api.DeviceOsSpec{
					Image: "newos",
				},
			},
			Status: &status,
		}

		raceCalled := false
		race := func() {
			if raceCalled {
				return
			}
			otherupdate := api.Device{Metadata: api.ObjectMeta{Name: lo.ToPtr("mydevice-1")}, Spec: &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "bah"}}}
			device, err := model.NewDeviceFromApiResource(&otherupdate)
			device.OrgID = orgId
			device.ResourceVersion = lo.ToPtr(int64(5))
			Expect(err).ToNot(HaveOccurred())
			result := db.Updates(device)
			Expect(result.Error).ToNot(HaveOccurred())
		}
		devStore.SetIntegrationTestCreateOrUpdateCallback(race)

		dev, created, _, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, nil, callback)
		Expect(err).ToNot(HaveOccurred())
		Expect(created).To(Equal(false))
		Expect(dev.ApiVersion).To(Equal(model.DeviceAPIVersion()))
		Expect(dev.Kind).To(Equal(api.DeviceKind))
		Expect(dev.Spec.Os.Image).To(Equal("newos"))
		Expect(dev.Metadata.ResourceVersion).ToNot(BeNil())
		Expect(*dev.Metadata.ResourceVersion).To(Equal("6"))
	})

	It("CreateOrUpdateDevice update with stale resourceVersion", func() {
		dev, err := devStore.Get(ctx, orgId, "mydevice-1")
		Expect(err).ToNot(HaveOccurred())
		dev.Metadata.Owner = lo.ToPtr("newowner")
		dev.Spec.Os.Image = "oldos"
		// Update but don't save the new device, so we still have the old resourceVersion
		dev, _, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, false, nil, callback)
		Expect(err).ToNot(HaveOccurred())
		Expect(called).To(BeTrue())

		dev.Spec.Os.Image = "newos"
		_, _, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, true, nil, callback)
		Expect(err).To(HaveOccurred())
		Expect(err).To(MatchError(flterrors.ErrUpdatingResourceWithOwnerNotAllowed))
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
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
		})

		It("Get device - wrong org - not found error", func() {
			badOrgId, _ := uuid.NewUUID()
			_, err := devStore.Get(ctx, badOrgId, "mydevice-1")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrResourceNotFound))
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
			otherOrgId, _ := uuid.NewUUID()
			err := devStore.DeleteAll(ctx, otherOrgId, allDeletedCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			listParams := store.ListParams{Limit: 1000}
			devices, err := devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(devices.Items).To(HaveLen(numDevices))

			called = false
			err = devStore.DeleteAll(ctx, orgId, allDeletedCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			devices, err = devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(devices.Items).To(HaveLen(0))
		})

		It("List with summary", func() {
			allDevices, err := devStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(allDevices.Items).To(HaveLen(3))
			expectedApplicationMap := make(map[string]int64)
			expectedSummaryMap := make(map[string]int64)
			expectedUpdatedMap := make(map[string]int64)
			for i := range allDevices.Items {
				d := &allDevices.Items[i]
				applicationStatus := fmt.Sprintf("application-%d", i)
				d.Status.ApplicationsSummary.Status = api.ApplicationsSummaryStatusType(applicationStatus)
				expectedApplicationMap[applicationStatus] = expectedApplicationMap[applicationStatus] + 1
				status := lo.Ternary(i%2 == 0, "status-1", "status-2")
				expectedSummaryMap[status] = expectedSummaryMap[status] + 1
				d.Status.Summary.Status = api.DeviceSummaryStatusType(status)
				updatedStatus := fmt.Sprintf("updated-%d", i)
				d.Status.Updated.Status = api.DeviceUpdatedStatusType(updatedStatus)
				expectedUpdatedMap[updatedStatus] = expectedUpdatedMap[updatedStatus] + 1
				_, err = devStore.UpdateStatus(ctx, orgId, d)
				Expect(err).ToNot(HaveOccurred())
			}
			allDevices, err = devStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(allDevices.Items).To(HaveLen(3))
			Expect(allDevices.Summary.ApplicationStatus).To(Equal(expectedApplicationMap))
			Expect(allDevices.Summary.SummaryStatus).To(Equal(expectedSummaryMap))
			Expect(allDevices.Summary.UpdateStatus).To(Equal(expectedUpdatedMap))
			Expect(allDevices.Summary.Total).To(Equal(int64(3)))

			allDevicesSummary, err := devStore.Summary(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(allDevicesSummary.ApplicationStatus).To(Equal(expectedApplicationMap))
			Expect(allDevicesSummary.SummaryStatus).To(Equal(expectedSummaryMap))
			Expect(allDevicesSummary.UpdateStatus).To(Equal(expectedUpdatedMap))
			Expect(allDevicesSummary.Total).To(Equal(int64(3)))
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
				Limit:         1000,
				LabelSelector: selector.NewLabelSelectorFromMapOrDie(map[string]string{"key": "value-1"})}
			devices, err := devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))
			Expect(*devices.Items[0].Metadata.Name).To(Equal("mydevice-1"))
		})

		It("List with status field filter paging", func() {
			listParams := store.ListParams{
				Limit:         1000,
				FieldSelector: selector.NewFieldSelectorOrDie("status.updated.status in (Unknown, Updating)", selector.WithPrivateSelectors()),
			}
			devices, err := devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(3))
		})

		It("List with owner selector", func() {
			testutil.CreateTestDevice(ctx, devStore, orgId, "fleet-a-device", lo.ToPtr("Fleet/fleet-a"), nil, nil)
			testutil.CreateTestDevice(ctx, devStore, orgId, "fleet-b-device", lo.ToPtr("Fleet/fleet-b"), nil, nil)
			listParams := store.ListParams{
				Limit: 1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(
					map[string]string{"metadata.owner": "Fleet/fleet-a"}, selector.WithPrivateSelectors()),
			}
			devices, err := devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))

			listParams = store.ListParams{
				Limit: 1000,
				FieldSelector: selector.NewFieldSelectorFromMapOrDie(
					map[string]string{"metadata.owner": "Fleet/fleet-b"}, selector.WithPrivateSelectors()),
			}
			devices, err = devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(1))

			listParams = store.ListParams{
				Limit:         1000,
				FieldSelector: selector.NewFieldSelectorOrDie("metadata.owner in (Fleet/fleet-a, Fleet/fleet-b)", selector.WithPrivateSelectors()),
			}
			devices, err = devStore.List(ctx, orgId, listParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(2))
		})

		It("CreateOrUpdateDevice create mode", func() {
			imageName := "tv"
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("newresourcename"),
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: imageName},
				},
				Status: nil,
			}
			dev, created, _, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(true))
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPIVersion()))
			Expect(dev.Kind).To(Equal(api.DeviceKind))
			Expect(dev.Spec.Os.Image).To(Equal(imageName))
		})

		It("CreateOrUpdateDevice update mode", func() {
			status := api.NewDeviceStatus()
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("mydevice-1"),
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{
						Image: "newos",
					},
				},
				Status: &status,
			}
			dev, created, _, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(Equal(false))
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPIVersion()))
			Expect(dev.Kind).To(Equal(api.DeviceKind))
			Expect(dev.Spec.Os.Image).To(Equal("newos"))
		})

		It("CreateOrUpdateDevice update owned from API", func() {
			dev, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			dev.Metadata.Owner = lo.ToPtr("newowner")
			dev.Spec.Os.Image = "oldos"
			dev, _, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			dev.Spec.Os.Image = "newos"
			_, _, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, true, nil, callback)
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrUpdatingResourceWithOwnerNotAllowed))
		})

		It("CreateOrUpdateDevice update labels owned from API", func() {
			testutil.CreateTestDevice(ctx, devStore, orgId, "owned-device", lo.ToPtr("ownerfleet"), nil, nil)
			dev, err := devStore.Get(ctx, orgId, "owned-device")
			Expect(err).ToNot(HaveOccurred())

			newDev := testutil.ReturnTestDevice(orgId, "owned-device", lo.ToPtr("ownerfleet"), nil, &map[string]string{"newkey": "newval"})
			newDev.Metadata.ResourceVersion = dev.Metadata.ResourceVersion

			_, _, _, err = devStore.CreateOrUpdate(ctx, orgId, &newDev, nil, true, nil, callback)

			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})

		It("UpdateDeviceStatus", func() {
			// Random Condition to make sure Conditions do get stored
			status := api.NewDeviceStatus()
			condition := api.Condition{
				Type:               api.DeviceUpdating,
				LastTransitionTime: time.Now(),
				Status:             api.ConditionStatusFalse,
				Reason:             "reason",
				Message:            "message",
			}
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("mydevice-1"),
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "newos"},
				},
				Status: &status,
			}
			api.SetStatusCondition(&device.Status.Conditions, condition)
			_, err := devStore.UpdateStatus(ctx, orgId, &device)
			Expect(err).ToNot(HaveOccurred())
			dev, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPIVersion()))
			Expect(dev.Kind).To(Equal(api.DeviceKind))
			Expect(dev.Spec.Os.Image).To(Equal("os"))
			Expect(dev.Status.Conditions).ToNot(BeEmpty())
			Expect(api.IsStatusConditionFalse(dev.Status.Conditions, api.DeviceUpdating)).To(BeTrue())
		})

		It("UpdateOwner", func() {
			dev, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())

			dev.Metadata.Owner = lo.ToPtr("newowner")
			_, _, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			dev, err = devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Owner).ToNot(BeNil())
			Expect(*dev.Metadata.Owner).To(Equal("newowner"))

			called = false
			dev.Metadata.Owner = nil
			_, _, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, []string{"owner"}, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			dev, err = devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Owner).To(BeNil())
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

		It("UpdateDeviceAnnotations console", func() {
			firstAnnotations := map[string]string{"key1": "val1"}
			err := devStore.UpdateAnnotations(ctx, orgId, "mydevice-1", firstAnnotations, nil)
			Expect(err).ToNot(HaveOccurred())
			dev, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Annotations).ToNot(BeNil())
			Expect(*dev.Metadata.Annotations).To(HaveLen(1))
			Expect((*dev.Metadata.Annotations)["key1"]).To(Equal("val1"))

			err = devStore.UpdateAnnotations(ctx, orgId, "mydevice-1", map[string]string{api.DeviceAnnotationConsole: "console"}, nil)
			Expect(err).ToNot(HaveOccurred())
			dev, err = devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Annotations).ToNot(BeNil())
			Expect(*dev.Metadata.Annotations).To(HaveLen(3))
			Expect((*dev.Metadata.Annotations)["key1"]).To(Equal("val1"))
			Expect((*dev.Metadata.Annotations)[api.DeviceAnnotationConsole]).To(Equal("console"))
			Expect((*dev.Metadata.Annotations)[api.DeviceAnnotationRenderedVersion]).To(Equal("1"))

			err = devStore.UpdateAnnotations(ctx, orgId, "mydevice-1", nil, []string{api.DeviceAnnotationConsole})
			Expect(err).ToNot(HaveOccurred())
			dev, err = devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Annotations).ToNot(BeNil())
			Expect(*dev.Metadata.Annotations).To(HaveLen(2))
			Expect((*dev.Metadata.Annotations)["key1"]).To(Equal("val1"))
			Expect((*dev.Metadata.Annotations)[api.DeviceAnnotationRenderedVersion]).To(Equal("2"))
		})

		It("GetRendered", func() {
			testutil.CreateTestDevice(ctx, storeInst.Device(), orgId, "dev", nil, nil, nil)

			// No rendered version
			_, err := devStore.GetRendered(ctx, orgId, "dev", nil, "")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrNoRenderedVersion))

			firstConfig, err := createTestConfigProvider("this is the first config")
			Expect(err).ToNot(HaveOccurred())

			fmt.Printf("firstConfig: %+v\n", firstConfig)

			// Set first rendered config
			err = devStore.UpdateRendered(ctx, orgId, "dev", firstConfig, "")
			Expect(err).ToNot(HaveOccurred())

			// Getting first rendered config
			renderedDevice, err := devStore.GetRendered(ctx, orgId, "dev", nil, "")
			fmt.Printf("renderedDevice: %+v\n", renderedDevice)
			Expect(err).ToNot(HaveOccurred())
			renderedConfig := *renderedDevice.Spec.Config
			Expect(len(renderedConfig)).To(BeNumerically(">", 0))
			provider, err := renderedConfig[0].AsInlineConfigProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(provider.Inline).ToNot(BeEmpty())
			Expect(provider.Inline[0].Content).To(Equal("this is the first config"))
			Expect(renderedDevice.Spec.Os.Image).To(Equal("os"))
			Expect(renderedDevice.Version()).To(Equal("1"))

			// Passing correct renderedVersion
			renderedDevice, err = devStore.GetRendered(ctx, orgId, "dev", lo.ToPtr("1"), "")
			Expect(err).ToNot(HaveOccurred())
			Expect(renderedDevice).To(BeNil())

			// Set second rendered config
			secondConfig, err := createTestConfigProvider("this is the second config")
			Expect(err).ToNot(HaveOccurred())
			err = devStore.UpdateRendered(ctx, orgId, "dev", secondConfig, "")
			Expect(err).ToNot(HaveOccurred())

			// Passing previous renderedVersion
			renderedDevice, err = devStore.GetRendered(ctx, orgId, "dev", lo.ToPtr("1"), "")
			Expect(err).ToNot(HaveOccurred())
			renderedConfig = *renderedDevice.Spec.Config
			Expect(len(renderedConfig)).To(BeNumerically(">", 0))
			provider, err = renderedConfig[0].AsInlineConfigProviderSpec()
			Expect(err).ToNot(HaveOccurred())
			Expect(provider.Inline).ToNot(BeEmpty())
			Expect(provider.Inline[0].Content).To(Equal("this is the second config"))
			Expect(renderedDevice.Spec.Os.Image).To(Equal("os"))
			Expect(renderedDevice.Version()).To(Equal("2"))
		})

		It("OverwriteRepositoryRefs", func() {
			err := testutil.CreateRepositories(ctx, 2, storeInst, orgId)
			Expect(err).ToNot(HaveOccurred())

			err = storeInst.Device().OverwriteRepositoryRefs(ctx, orgId, "mydevice-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Device().GetRepositoryRefs(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			devs, err := storeInst.Repository().GetDeviceRefs(ctx, orgId, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(devs.Items).To(HaveLen(1))
			Expect(*(devs.Items[0]).Metadata.Name).To(Equal("mydevice-1"))

			err = storeInst.Device().OverwriteRepositoryRefs(ctx, orgId, "mydevice-1", "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			repos, err = storeInst.Device().GetRepositoryRefs(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-2"))

			devs, err = storeInst.Repository().GetDeviceRefs(ctx, orgId, "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(devs.Items).To(HaveLen(0))

			devs, err = storeInst.Repository().GetDeviceRefs(ctx, orgId, "myrepository-2")
			Expect(err).ToNot(HaveOccurred())
			Expect(devs.Items).To(HaveLen(1))
			Expect(*(devs.Items[0]).Metadata.Name).To(Equal("mydevice-1"))
		})

		It("Delete device with repo association", func() {
			err := testutil.CreateRepositories(ctx, 1, storeInst, orgId)
			Expect(err).ToNot(HaveOccurred())

			err = storeInst.Device().OverwriteRepositoryRefs(ctx, orgId, "mydevice-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Device().GetRepositoryRefs(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			err = devStore.Delete(ctx, orgId, "mydevice-1", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})

		It("Delete all devices with repo association", func() {
			err := testutil.CreateRepositories(ctx, 1, storeInst, orgId)
			Expect(err).ToNot(HaveOccurred())

			err = storeInst.Device().OverwriteRepositoryRefs(ctx, orgId, "mydevice-1", "myrepository-1")
			Expect(err).ToNot(HaveOccurred())
			repos, err := storeInst.Device().GetRepositoryRefs(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*(repos.Items[0]).Metadata.Name).To(Equal("myrepository-1"))

			err = devStore.DeleteAll(ctx, orgId, allDeletedCallback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})
	})
})

func createTestConfigProvider(contents string) (string, error) {
	provider := api.ConfigProviderSpec{}
	files := []v1alpha1.FileSpec{
		{
			Content: contents,
		},
	}
	if err := provider.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{Inline: files}); err != nil {
		return "", err
	}

	providers := &[]api.ConfigProviderSpec{provider}
	providersBytes, err := json.Marshal(providers)
	if err != nil {
		return "", err
	}
	return string(providersBytes), nil
}
