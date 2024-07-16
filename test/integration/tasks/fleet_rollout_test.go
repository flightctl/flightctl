package tasks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

func TestController(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Tasks Suite")
}

var _ = Describe("FleetRollout", func() {
	var (
		log             *logrus.Logger
		ctx             context.Context
		orgId           uuid.UUID
		deviceStore     store.Device
		fleetStore      store.Fleet
		tvStore         store.TemplateVersion
		storeInst       store.Store
		cfg             *config.Config
		dbName          string
		numDevices      int
		fleetName       string
		callback        store.FleetStoreCallback
		callbackManager tasks.CallbackManager
		mockPublisher   *queues.MockPublisher
		ctrl            *gomock.Controller
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		numDevices = 3
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(log)
		deviceStore = storeInst.Device()
		fleetStore = storeInst.Fleet()
		tvStore = storeInst.TemplateVersion()
		fleetName = "myfleet"
		callback = func(before *model.Fleet, after *model.Fleet) {}
		ctrl = gomock.NewController(GinkgoT())
		mockPublisher = queues.NewMockPublisher(ctrl)
		callbackManager = tasks.NewCallbackManager(mockPublisher, log)
		mockPublisher.EXPECT().Publish(gomock.Any()).AnyTimes()
	})

	AfterEach(func() {
		store.DeleteTestDB(log, cfg, storeInst, dbName)
		ctrl.Finish()
	})

	When("the fleet is valid", func() {
		It("its devices are rolled out successfully", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
			err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.bad", "my bad OS", false)
			Expect(err).ToNot(HaveOccurred())
			testutil.CreateTestDevices(ctx, numDevices, deviceStore, orgId, util.StrToPtr("Fleet/myfleet"), true)
			fleet, err := fleetStore.Get(ctx, orgId, fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))

			devices, err := deviceStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(len(devices.Items)).To(Equal(numDevices))

			// First update
			logic := tasks.NewFleetRolloutsLogic(callbackManager, log, storeInst, tasks.ResourceReference{OrgID: orgId, Name: *fleet.Metadata.Name})
			logic.SetItemsPerPage(2)

			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", "my first OS", true)
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
			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.1", "my new OS", true)
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

		It("a new device is rolled out correctly", func() {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
			err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.bad", "my bad OS", false)
			Expect(err).ToNot(HaveOccurred())
			testutil.CreateTestDevice(ctx, deviceStore, orgId, "mydevice-1", util.StrToPtr("Fleet/myfleet"), nil, nil)
			fleet, err := fleetStore.Get(ctx, orgId, fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
			Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))

			logic := tasks.NewFleetRolloutsLogic(callbackManager, log, storeInst, tasks.ResourceReference{OrgID: orgId, Name: "mydevice-1"})
			logic.SetItemsPerPage(2)

			err = testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", "my first OS", true)
			Expect(err).ToNot(HaveOccurred())
			err = logic.RolloutDevice(ctx)
			Expect(err).ToNot(HaveOccurred())
			dev, err := deviceStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Annotations).ToNot(BeNil())
			Expect((*dev.Metadata.Annotations)[model.DeviceAnnotationTemplateVersion]).To(Equal("1.0.0"))
		})

		When("the fleet is valid and contains parameters", func() {
			var (
				gitConfig    *api.GitConfigProviderSpec
				inlineConfig *api.InlineConfigProviderSpec
			)

			BeforeEach(func() {
				gitConfig = &api.GitConfigProviderSpec{
					ConfigType: string(api.TemplateDiscriminatorGitConfig),
					Name:       "paramGitConfig",
				}
				gitConfig.GitRef.Path = "path-{{ device.metadata.labels[key] }}"
				gitConfig.GitRef.Repository = "repo"
				gitConfig.GitRef.TargetRevision = "rev"

				inlineConfig = &api.InlineConfigProviderSpec{
					ConfigType: string(api.TemplateDiscriminatorInlineConfig),
					Name:       "paramInlineConfig",
				}
				var inline map[string]interface{}
				err := json.Unmarshal([]byte("{\"ignition\": {\"version\": \"3.4.{{ device.metadata.labels[version] }}\"}}"), &inline)
				Expect(err).ToNot(HaveOccurred())
				inlineConfig.Inline = inline
			})

			It("its devices are rolled out successfully", func() {
				// Create fleet and TV
				testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
				err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0", "myOS", true)
				Expect(err).ToNot(HaveOccurred())

				// Update the TV with git and inline configs, both with parameters
				tv, err := storeInst.TemplateVersion().Get(ctx, orgId, fleetName, "1.0")
				Expect(err).ToNot(HaveOccurred())
				gitItem := api.TemplateVersionStatus_Config_Item{}
				err = gitItem.FromGitConfigProviderSpec(*gitConfig)
				Expect(err).ToNot(HaveOccurred())
				inlineItem := api.TemplateVersionStatus_Config_Item{}
				err = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)
				Expect(err).ToNot(HaveOccurred())
				tv.Status.Config = &[]api.TemplateVersionStatus_Config_Item{gitItem, inlineItem}
				tvCallback := store.TemplateVersionStoreCallback(func(tv *model.TemplateVersion) {})
				err = storeInst.TemplateVersion().UpdateStatus(ctx, orgId, tv, util.BoolToPtr(true), tvCallback)
				Expect(err).ToNot(HaveOccurred())

				// Add devices to the fleet
				testutil.CreateTestDevices(ctx, numDevices, deviceStore, orgId, util.StrToPtr("Fleet/myfleet"), false)
				fleet, err := fleetStore.Get(ctx, orgId, fleetName)
				Expect(err).ToNot(HaveOccurred())
				Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
				Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))

				devices, err := deviceStore.List(ctx, orgId, store.ListParams{})
				Expect(err).ToNot(HaveOccurred())
				Expect(len(devices.Items)).To(Equal(numDevices))

				// Roll out the devices and check their configs
				logic := tasks.NewFleetRolloutsLogic(callbackManager, log, storeInst, tasks.ResourceReference{OrgID: orgId, Name: *fleet.Metadata.Name})
				err = logic.RolloutFleet(ctx)
				Expect(err).ToNot(HaveOccurred())
				for i := 1; i <= numDevices; i++ {
					dev, err := deviceStore.Get(ctx, orgId, fmt.Sprintf("mydevice-%d", i))
					Expect(err).ToNot(HaveOccurred())
					Expect(dev.Metadata.Annotations).ToNot(BeNil())
					Expect((*dev.Metadata.Annotations)[model.DeviceAnnotationTemplateVersion]).To(Equal("1.0"))
					Expect(dev.Spec.Config).ToNot(BeNil())
					Expect(*dev.Spec.Config).To(HaveLen(2))
					for _, configItem := range *dev.Spec.Config {
						disc, err := configItem.Discriminator()
						Expect(err).ToNot(HaveOccurred())
						switch disc {
						case string(api.TemplateDiscriminatorGitConfig):
							gitSpec, err := configItem.AsGitConfigProviderSpec()
							Expect(err).ToNot(HaveOccurred())
							Expect(gitSpec.GitRef.Path).To(Equal(fmt.Sprintf("path-value-%d", i)))
						case string(api.TemplateDiscriminatorInlineConfig):
							inlineSpec, err := configItem.AsInlineConfigProviderSpec()
							Expect(err).ToNot(HaveOccurred())
							ig := inlineSpec.Inline["ignition"]
							igMap, ok := ig.(map[string]interface{})
							Expect(ok).To(BeTrue())
							ver := igMap["version"]
							verStr, ok := ver.(string)
							Expect(ok).To(BeTrue())
							Expect(verStr).To(Equal(fmt.Sprintf("3.4.%d", i)))
						default:
							Expect("").To(Equal("unexpected discriminator"))
						}
					}
				}
			})

			It("a new device is rolled out correctly", func() {
				// Create fleet and TV
				testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
				err := testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0", "myOS", true)
				Expect(err).ToNot(HaveOccurred())

				// Update the TV with git and inline configs, both with parameters
				tv, err := storeInst.TemplateVersion().Get(ctx, orgId, fleetName, "1.0")
				Expect(err).ToNot(HaveOccurred())
				gitItem := api.TemplateVersionStatus_Config_Item{}
				err = gitItem.FromGitConfigProviderSpec(*gitConfig)
				Expect(err).ToNot(HaveOccurred())
				inlineItem := api.TemplateVersionStatus_Config_Item{}
				err = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)
				Expect(err).ToNot(HaveOccurred())
				tv.Status.Config = &[]api.TemplateVersionStatus_Config_Item{gitItem, inlineItem}
				tvCallback := store.TemplateVersionStoreCallback(func(tv *model.TemplateVersion) {})
				err = storeInst.TemplateVersion().UpdateStatus(ctx, orgId, tv, util.BoolToPtr(true), tvCallback)
				Expect(err).ToNot(HaveOccurred())

				// Add a device to the fleet
				labels := map[string]string{"key": "some-value", "otherkey": "other-value", "version": "2"}
				testutil.CreateTestDevice(ctx, deviceStore, orgId, "mydevice-1", util.StrToPtr("Fleet/myfleet"), nil, &labels)
				fleet, err := fleetStore.Get(ctx, orgId, fleetName)
				Expect(err).ToNot(HaveOccurred())
				Expect(*fleet.Metadata.Generation).To(Equal(int64(1)))
				Expect(*fleet.Spec.Template.Metadata.Generation).To(Equal(int64(1)))

				// Roll out to the single device
				logic := tasks.NewFleetRolloutsLogic(callbackManager, log, storeInst, tasks.ResourceReference{OrgID: orgId, Name: "mydevice-1"})
				err = logic.RolloutDevice(ctx)
				Expect(err).ToNot(HaveOccurred())
				dev, err := deviceStore.Get(ctx, orgId, "mydevice-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(dev.Metadata.Annotations).ToNot(BeNil())
				Expect((*dev.Metadata.Annotations)[model.DeviceAnnotationTemplateVersion]).To(Equal("1.0"))
				Expect(dev.Spec.Config).ToNot(BeNil())
				Expect(*dev.Spec.Config).To(HaveLen(2))
				for _, configItem := range *dev.Spec.Config {
					disc, err := configItem.Discriminator()
					Expect(err).ToNot(HaveOccurred())
					switch disc {
					case string(api.TemplateDiscriminatorGitConfig):
						gitSpec, err := configItem.AsGitConfigProviderSpec()
						Expect(err).ToNot(HaveOccurred())
						Expect(gitSpec.GitRef.Path).To(Equal("path-some-value"))
					case string(api.TemplateDiscriminatorInlineConfig):
						inlineSpec, err := configItem.AsInlineConfigProviderSpec()
						Expect(err).ToNot(HaveOccurred())
						ig := inlineSpec.Inline["ignition"]
						igMap, ok := ig.(map[string]interface{})
						Expect(ok).To(BeTrue())
						ver := igMap["version"]
						verStr, ok := ver.(string)
						Expect(ok).To(BeTrue())
						Expect(verStr).To(Equal("3.4.2"))
					default:
						Expect("").To(Equal("unexpected discriminator"))
					}
				}
			})
		})
	})
})
