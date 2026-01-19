package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
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

var (
	suiteCtx context.Context
)

func TestStore(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Store Suite")
}

var _ = BeforeSuite(func() {
	suiteCtx = testutil.InitSuiteTracerForGinkgo("Store Suite")
})

var _ = Describe("DeviceStore create", func() {
	var (
		log        *logrus.Logger
		ctx        context.Context
		orgId      uuid.UUID
		storeInst  store.Store
		devStore   store.Device
		cfg        *config.Config
		db         *gorm.DB
		dbName     string
		numDevices int
		called     bool
		callback   store.EventCallback
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		numDevices = 3
		storeInst, cfg, dbName, db = store.PrepareDBForUnitTests(ctx, log)
		devStore = storeInst.Device()
		called = false
		callback = store.EventCallback(func(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
			called = true
		})

		orgId = uuid.New()
		err := testutil.CreateTestOrganization(ctx, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())

		testutil.CreateTestDevices(ctx, 3, devStore, orgId, nil, false)
	})

	AfterEach(func() {
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
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
			result := db.WithContext(ctx).Create(&model.Device{Resource: model.Resource{OrgID: orgId, Name: "newresourcename", ResourceVersion: lo.ToPtr(int64(1))}, Spec: model.MakeJSONField(api.DeviceSpec{})})
			Expect(result.Error).ToNot(HaveOccurred())
		}
		devStore.SetIntegrationTestCreateOrUpdateCallback(race)

		_, created, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, nil, callback)
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
			result := db.WithContext(ctx).Updates(device)
			Expect(result.Error).ToNot(HaveOccurred())
		}
		devStore.SetIntegrationTestCreateOrUpdateCallback(race)

		dev, created, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, nil, callback)
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
		dev, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, false, nil, callback)
		Expect(err).ToNot(HaveOccurred())
		Expect(called).To(BeTrue())

		dev.Spec.Os.Image = "newos"
		_, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, true, nil, callback)
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
			deleted, err := devStore.Delete(ctx, orgId, "mydevice-1", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeTrue())
			Expect(called).To(BeTrue())
		})

		It("Delete device success when not found", func() {
			deleted, err := devStore.Delete(ctx, orgId, "nonexistent", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeFalse())
			Expect(called).To(BeFalse())
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
				_, err = devStore.UpdateStatus(ctx, orgId, d, nil)
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
			dev, created, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, nil, callback)
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
			dev, created, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, nil, callback)
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
			dev, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			dev.Spec.Os.Image = "newos"
			_, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, true, nil, callback)
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrUpdatingResourceWithOwnerNotAllowed))
		})

		It("CreateOrUpdateDevice update labels owned from API", func() {
			// Create a comprehensive DeviceSpec with all possible fields to test our comparison logic
			createComprehensiveTestDevice := func(orgId uuid.UUID, name string, owner *string, labels *map[string]string) api.Device {
				// Create OS spec
				osSpec := &api.DeviceOsSpec{
					Image: "quay.io/flightctl/comprehensive-os:latest",
				}

				// Create all types of config providers (union types)
				gitConfig := &api.GitConfigProviderSpec{
					Name: "git-config",
					GitRef: struct {
						Path           string `json:"path"`
						Repository     string `json:"repository"`
						TargetRevision string `json:"targetRevision"`
					}{
						Path:           "/config/git",
						Repository:     "test-repo",
						TargetRevision: "main",
					},
				}
				gitItem := api.ConfigProviderSpec{}
				_ = gitItem.FromGitConfigProviderSpec(*gitConfig)

				inlineConfig := &api.InlineConfigProviderSpec{
					Name: "inline-config",
					Inline: []api.FileSpec{
						{
							Path:    "/etc/test-config",
							Content: "test configuration content",
						},
					},
				}
				inlineItem := api.ConfigProviderSpec{}
				_ = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)

				httpConfig := &api.HttpConfigProviderSpec{
					Name: "http-config",
					HttpRef: struct {
						FilePath   string  `json:"filePath"`
						Repository string  `json:"repository"`
						Suffix     *string `json:"suffix,omitempty"`
					}{
						FilePath:   "/config/http",
						Repository: "http-repo",
						Suffix:     lo.ToPtr("/config.yaml"),
					},
				}
				httpItem := api.ConfigProviderSpec{}
				_ = httpItem.FromHttpConfigProviderSpec(*httpConfig)

				// Create application providers (union types)
				// Create application volumes (union types)
				imageVolume := api.ApplicationVolume{
					Name: "test-image-volume",
				}
				_ = imageVolume.FromImageVolumeProviderSpec(api.ImageVolumeProviderSpec{
					Image: api.ImageVolumeSource{
						Reference:  "quay.io/flightctl/test-volume:latest",
						PullPolicy: lo.ToPtr(api.PullIfNotPresent),
					},
				})

				imageComposeApp := api.ComposeApplication{
					Name:    lo.ToPtr("test-image-app"),
					AppType: api.AppTypeCompose,
					Volumes: &[]api.ApplicationVolume{imageVolume},
				}
				_ = imageComposeApp.FromImageApplicationProviderSpec(api.ImageApplicationProviderSpec{
					Image: "quay.io/flightctl/test-app:latest",
				})
				var imageAppItem api.ApplicationProviderSpec
				_ = imageAppItem.FromComposeApplication(imageComposeApp)

				inlineComposeApp := api.ComposeApplication{
					Name:    lo.ToPtr("test-inline-app"),
					AppType: api.AppTypeCompose,
					Volumes: &[]api.ApplicationVolume{imageVolume}, // Reuse the same volume
				}
				_ = inlineComposeApp.FromInlineApplicationProviderSpec(api.InlineApplicationProviderSpec{
					Inline: []api.ApplicationContent{
						{
							Path:    "docker-compose.yaml",
							Content: lo.ToPtr("version: '3'\nservices:\n  test:\n    image: alpine\n"),
						},
					},
				})
				var inlineAppItem api.ApplicationProviderSpec
				_ = inlineAppItem.FromComposeApplication(inlineComposeApp)

				// Create resource monitors (union types)
				cpuMonitor := api.ResourceMonitor{}
				_ = cpuMonitor.FromCpuResourceMonitorSpec(api.CpuResourceMonitorSpec{
					MonitorType:      "CPU",
					SamplingInterval: "30s",
					AlertRules: []api.ResourceAlertRule{
						{
							Severity:    api.ResourceAlertSeverityTypeCritical,
							Percentage:  90.0,
							Duration:    "5m",
							Description: "High CPU usage",
						},
					},
				})

				memoryMonitor := api.ResourceMonitor{}
				_ = memoryMonitor.FromMemoryResourceMonitorSpec(api.MemoryResourceMonitorSpec{
					MonitorType:      "Memory",
					SamplingInterval: "30s",
					AlertRules: []api.ResourceAlertRule{
						{
							Severity:    api.ResourceAlertSeverityTypeWarning,
							Percentage:  80.0,
							Duration:    "10m",
							Description: "High memory usage",
						},
					},
				})

				diskMonitor := api.ResourceMonitor{}
				_ = diskMonitor.FromDiskResourceMonitorSpec(api.DiskResourceMonitorSpec{
					MonitorType:      "Disk",
					Path:             "/",
					SamplingInterval: "60s",
					AlertRules: []api.ResourceAlertRule{
						{
							Severity:    api.ResourceAlertSeverityTypeWarning,
							Percentage:  85.0,
							Duration:    "15m",
							Description: "High disk usage",
						},
					},
				})

				// Create consoles
				consoles := []api.DeviceConsole{
					{
						SessionID:       "session-123",
						SessionMetadata: "terminal=xterm",
					},
				}

				// Create decommissioning spec
				decommissioning := &api.DeviceDecommission{
					Target: api.DeviceDecommissionTargetTypeUnenroll,
				}

				// Create systemd spec
				systemd := &struct {
					MatchPatterns *[]string `json:"matchPatterns,omitempty"`
				}{
					MatchPatterns: &[]string{"systemd-*", "docker.service"},
				}

				// Create update policy
				updatePolicy := &api.DeviceUpdatePolicySpec{
					DownloadSchedule: &api.UpdateSchedule{
						At:       "0 2 * * *",
						TimeZone: lo.ToPtr("UTC"),
					},
					UpdateSchedule: &api.UpdateSchedule{
						At:       "0 3 * * *",
						TimeZone: lo.ToPtr("UTC"),
					},
				}

				return api.Device{
					Metadata: api.ObjectMeta{
						Name:   &name,
						Labels: labels,
						Owner:  owner,
					},
					Spec: &api.DeviceSpec{
						Os:              osSpec,
						Config:          &[]api.ConfigProviderSpec{gitItem, inlineItem, httpItem},
						Applications:    &[]api.ApplicationProviderSpec{imageAppItem, inlineAppItem},
						Resources:       &[]api.ResourceMonitor{cpuMonitor, memoryMonitor, diskMonitor},
						Consoles:        &consoles,
						Decommissioning: decommissioning,
						Systemd:         systemd,
						UpdatePolicy:    updatePolicy,
					},
				}
			}

			// Create the first device with comprehensive spec
			device1 := createComprehensiveTestDevice(orgId, "owned-device", lo.ToPtr("ownerfleet"), nil)
			_, _, err := devStore.CreateOrUpdate(ctx, orgId, &device1, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Get the device from the store
			dev, err := devStore.Get(ctx, orgId, "owned-device")
			Expect(err).ToNot(HaveOccurred())

			// Create the second device with the same comprehensive spec but different labels
			newDev := createComprehensiveTestDevice(orgId, "owned-device", lo.ToPtr("ownerfleet"), &map[string]string{"newkey": "newval"})
			newDev.Metadata.ResourceVersion = dev.Metadata.ResourceVersion

			// This should succeed because only labels (metadata) are different, not the spec
			_, _, err = devStore.CreateOrUpdate(ctx, orgId, &newDev, nil, true, nil, callback)

			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())
		})

		It("UpdateDeviceStatus", func() {
			// Random Condition to make sure Conditions do get stored
			status := api.NewDeviceStatus()
			condition := api.Condition{
				Type:               api.ConditionTypeDeviceUpdating,
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
			_, err := devStore.UpdateStatus(ctx, orgId, &device, nil)
			Expect(err).ToNot(HaveOccurred())
			dev, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.ApiVersion).To(Equal(model.DeviceAPIVersion()))
			Expect(dev.Kind).To(Equal(api.DeviceKind))
			Expect(dev.Spec.Os.Image).To(Equal("os"))
			Expect(dev.Status.Conditions).ToNot(BeEmpty())
			Expect(api.IsStatusConditionFalse(dev.Status.Conditions, api.ConditionTypeDeviceUpdating)).To(BeTrue())
		})

		It("UpdateOwner", func() {
			dev, err := devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())

			dev.Metadata.Owner = lo.ToPtr("newowner")
			_, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(called).To(BeTrue())

			dev, err = devStore.Get(ctx, orgId, "mydevice-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(dev.Metadata.Owner).ToNot(BeNil())
			Expect(*dev.Metadata.Owner).To(Equal("newowner"))

			called = false
			dev.Metadata.Owner = nil
			_, _, err = devStore.CreateOrUpdate(ctx, orgId, dev, []string{"owner"}, false, nil, callback)
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

		Context("Device Resume Operations", func() {

			Describe("RemoveConflictPausedAnnotation", func() {
				var (
					labelSelector *selector.LabelSelector
					listParams    store.ListParams
					testId        string
					testDevices   []struct {
						name        string
						labels      map[string]string
						hasConflict bool
					}
				)

				BeforeEach(func() {
					// Generate unique test ID for this test run
					testId = uuid.New().String()

					// Create test devices with unique names and labels
					testDevices = []struct {
						name        string
						labels      map[string]string
						hasConflict bool
					}{
						{fmt.Sprintf("bulk-test-device-1-%s", testId), map[string]string{fmt.Sprintf("env-%s", testId): "staging", fmt.Sprintf("tier-%s", testId): "web"}, true},
						{fmt.Sprintf("bulk-test-device-2-%s", testId), map[string]string{fmt.Sprintf("env-%s", testId): "staging", fmt.Sprintf("tier-%s", testId): "api"}, true},
						{fmt.Sprintf("bulk-test-device-3-%s", testId), map[string]string{fmt.Sprintf("env-%s", testId): "production", fmt.Sprintf("tier-%s", testId): "web"}, true},
						{fmt.Sprintf("bulk-test-device-4-%s", testId), map[string]string{fmt.Sprintf("env-%s", testId): "staging", fmt.Sprintf("tier-%s", testId): "web"}, false}, // no conflict annotation
						{fmt.Sprintf("bulk-test-device-5-%s", testId), map[string]string{fmt.Sprintf("env-%s", testId): "development", fmt.Sprintf("tier-%s", testId): "web"}, true},
					}

					for _, d := range testDevices {
						device := api.Device{
							Metadata: api.ObjectMeta{
								Name:   lo.ToPtr(d.name),
								Labels: &d.labels,
							},
							Spec: &api.DeviceSpec{
								Os: &api.DeviceOsSpec{Image: "test-os"},
							},
						}

						_, _, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, false, nil, callback)
						Expect(err).ToNot(HaveOccurred())

						if d.hasConflict {
							err = devStore.UpdateAnnotations(ctx, orgId, d.name,
								map[string]string{api.DeviceAnnotationConflictPaused: "true"}, nil)
							Expect(err).ToNot(HaveOccurred())
						}
					}
				})

				It("should remove annotation from devices matching label selector", func() {
					// Create label selector using the unique test ID
					var err error
					labelSelector, err = selector.NewLabelSelector(fmt.Sprintf("env-%s=staging", testId))
					Expect(err).ToNot(HaveOccurred())

					listParams = store.ListParams{
						LabelSelector: labelSelector,
					}

					// Resume devices with env=staging
					count, _, err := devStore.RemoveConflictPausedAnnotation(ctx, orgId, listParams)
					Expect(err).ToNot(HaveOccurred())
					Expect(count).To(Equal(int64(2))) // device-1 and device-2 have annotation

					// Verify correct devices had annotation removed
					dev1, err := devStore.Get(ctx, orgId, testDevices[0].name) // device-1
					Expect(err).ToNot(HaveOccurred())
					_, hasAnnotation := (*dev1.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
					Expect(hasAnnotation).To(BeFalse())

					dev2, err := devStore.Get(ctx, orgId, testDevices[1].name) // device-2
					Expect(err).ToNot(HaveOccurred())
					_, hasAnnotation = (*dev2.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
					Expect(hasAnnotation).To(BeFalse())

					// Verify devices not matching selector still have annotation
					dev3, err := devStore.Get(ctx, orgId, testDevices[2].name) // device-3 (production)
					Expect(err).ToNot(HaveOccurred())
					_, hasAnnotation = (*dev3.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
					Expect(hasAnnotation).To(BeTrue())

					// Verify device without annotation was not affected
					dev4, err := devStore.Get(ctx, orgId, testDevices[3].name) // device-4 (no annotation)
					Expect(err).ToNot(HaveOccurred())
					_, hasAnnotation = (*dev4.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
					Expect(hasAnnotation).To(BeFalse())
				})

				It("should return zero count when no devices match selector", func() {
					labelSelector, err := selector.NewLabelSelector(fmt.Sprintf("env-%s=nonexistent", testId))
					Expect(err).ToNot(HaveOccurred())

					listParams = store.ListParams{
						LabelSelector: labelSelector,
					}

					count, _, err := devStore.RemoveConflictPausedAnnotation(ctx, orgId, listParams)
					Expect(err).ToNot(HaveOccurred())
					Expect(count).To(Equal(int64(0)))
				})

				It("should return zero count when matching devices have no conflictPaused annotation", func() {
					// Select device-4 which matches but has no conflictPaused annotation
					labelSelector, err := selector.NewLabelSelector(fmt.Sprintf("env-%s=staging,tier-%s=web", testId, testId))
					Expect(err).ToNot(HaveOccurred())

					// First remove annotation from device-1 using bulk method to leave only device-4 matching
					device1Selector, err := selector.NewLabelSelector(fmt.Sprintf("env-%s=staging,tier-%s=web", testId, testId))
					Expect(err).ToNot(HaveOccurred())

					// Create a more specific selector that only matches device-1
					device1FieldSelector, err := selector.NewFieldSelectorFromMap(map[string]string{"metadata.name": testDevices[0].name})
					Expect(err).ToNot(HaveOccurred())

					device1ListParams := store.ListParams{
						LabelSelector: device1Selector,
						FieldSelector: device1FieldSelector,
					}
					_, _, err = devStore.RemoveConflictPausedAnnotation(ctx, orgId, device1ListParams)
					Expect(err).ToNot(HaveOccurred())

					listParams = store.ListParams{
						LabelSelector: labelSelector,
					}

					count, _, err := devStore.RemoveConflictPausedAnnotation(ctx, orgId, listParams)
					Expect(err).ToNot(HaveOccurred())
					Expect(count).To(Equal(int64(0))) // device-4 matches but has no annotation
				})

				It("should handle complex label selectors with multiple conditions", func() {
					// Select devices with env=staging AND tier!=api (should match device-1 and device-4)
					labelSelector, err := selector.NewLabelSelector(fmt.Sprintf("env-%s=staging,tier-%s!=api", testId, testId))
					Expect(err).ToNot(HaveOccurred())

					listParams = store.ListParams{
						LabelSelector: labelSelector,
					}

					count, _, err := devStore.RemoveConflictPausedAnnotation(ctx, orgId, listParams)
					Expect(err).ToNot(HaveOccurred())
					Expect(count).To(Equal(int64(1))) // Only device-1 has both matching labels AND annotation

					// Verify device-1 annotation removed
					dev1, err := devStore.Get(ctx, orgId, testDevices[0].name)
					Expect(err).ToNot(HaveOccurred())
					_, hasAnnotation := (*dev1.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
					Expect(hasAnnotation).To(BeFalse())

					// Verify device-2 still has annotation (doesn't match tier!=api)
					dev2, err := devStore.Get(ctx, orgId, testDevices[1].name)
					Expect(err).ToNot(HaveOccurred())
					_, hasAnnotation = (*dev2.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
					Expect(hasAnnotation).To(BeTrue())
				})

				It("should increment resource version for updated devices", func() {
					// Get initial versions for devices that will be updated
					dev1, err := devStore.Get(ctx, orgId, testDevices[0].name)
					Expect(err).ToNot(HaveOccurred())
					initialVersion1 := *dev1.Metadata.ResourceVersion

					dev2, err := devStore.Get(ctx, orgId, testDevices[1].name)
					Expect(err).ToNot(HaveOccurred())
					initialVersion2 := *dev2.Metadata.ResourceVersion

					// Resume devices with env=staging
					labelSelector, err := selector.NewLabelSelector(fmt.Sprintf("env-%s=staging", testId))
					Expect(err).ToNot(HaveOccurred())

					listParams = store.ListParams{
						LabelSelector: labelSelector,
					}

					count, _, err := devStore.RemoveConflictPausedAnnotation(ctx, orgId, listParams)
					Expect(err).ToNot(HaveOccurred())
					Expect(count).To(Equal(int64(2)))

					// Verify resource versions incremented for updated devices
					dev1, err = devStore.Get(ctx, orgId, testDevices[0].name)
					Expect(err).ToNot(HaveOccurred())
					Expect(*dev1.Metadata.ResourceVersion).ToNot(Equal(initialVersion1))

					dev2, err = devStore.Get(ctx, orgId, testDevices[1].name)
					Expect(err).ToNot(HaveOccurred())
					Expect(*dev2.Metadata.ResourceVersion).ToNot(Equal(initialVersion2))

					// Verify devices not updated have unchanged versions
					dev3, err := devStore.Get(ctx, orgId, testDevices[2].name)
					Expect(err).ToNot(HaveOccurred())
					Expect(*dev3.Metadata.ResourceVersion).To(Equal("2")) // Should be version after annotation was added

					dev4, err := devStore.Get(ctx, orgId, testDevices[3].name)
					Expect(err).ToNot(HaveOccurred())
					Expect(*dev4.Metadata.ResourceVersion).To(Equal("1")) // Should be initial version (no annotation was added)
				})

				It("should handle empty label selector (match all devices)", func() {
					// Empty label selector should match all devices
					labelSelector, err := selector.NewLabelSelector("")
					Expect(err).ToNot(HaveOccurred())

					listParams = store.ListParams{
						LabelSelector: labelSelector,
					}

					count, _, err := devStore.RemoveConflictPausedAnnotation(ctx, orgId, listParams)
					Expect(err).ToNot(HaveOccurred())
					// Should match all devices with conflictPaused annotation (4 out of 5 test devices)
					// Since we're using unique devices per test, we know exactly how many should match
					Expect(count).To(Equal(int64(4))) // The 4 test devices with annotations
				})
			})
		})

		It("GetRendered", func() {
			testutil.CreateTestDevice(ctx, storeInst.Device(), orgId, "dev", nil, nil, nil)

			// No rendered version
			_, err := devStore.GetRendered(ctx, orgId, "dev", nil, "")
			Expect(err).To(HaveOccurred())
			Expect(err).Should(MatchError(flterrors.ErrNoRenderedVersion))

			firstConfig, err := createTestConfigProvider("this is the first config")
			Expect(err).ToNot(HaveOccurred())

			// Set first rendered config
			_, err = devStore.UpdateRendered(ctx, orgId, "dev", firstConfig, "", "hash1")
			Expect(err).ToNot(HaveOccurred())

			// Getting first rendered config
			renderedDevice, err := devStore.GetRendered(ctx, orgId, "dev", nil, "")
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
			_, err = devStore.UpdateRendered(ctx, orgId, "dev", secondConfig, "", "hash2")
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

			deleted, err := devStore.Delete(ctx, orgId, "mydevice-1", callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(deleted).To(BeTrue())
			Expect(called).To(BeTrue())
		})

		It("DeviceSpecsAreEqual integration scenarios", func() {
			// Test DeviceSpecsAreEqual in realistic database scenarios

			// Create a device with complex spec including union types and maps
			gitConfig := api.ConfigProviderSpec{}
			err := gitConfig.FromGitConfigProviderSpec(api.GitConfigProviderSpec{
				Name: "test-git-config",
				GitRef: struct {
					Path           string `json:"path"`
					Repository     string `json:"repository"`
					TargetRevision string `json:"targetRevision"`
				}{
					Path:           "/config/path",
					Repository:     "test-repo",
					TargetRevision: "main",
				},
			})
			Expect(err).ToNot(HaveOccurred())

			inlineConfig := api.ConfigProviderSpec{}
			err = inlineConfig.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{
				Name: "test-inline-config",
				Inline: []api.FileSpec{
					{Path: "/file1.yaml", Content: "key1: value1"},
					{Path: "/file2.yaml", Content: "key2: value2"},
				},
			})
			Expect(err).ToNot(HaveOccurred())

			// Create compose application with env vars
			testComposeApp := api.ComposeApplication{
				Name:    lo.ToPtr("test-app"),
				AppType: api.AppTypeCompose,
				EnvVars: &map[string]string{
					"ENV1": "value1",
					"ENV2": "value2",
					"ENV3": "value3",
				},
			}
			_ = testComposeApp.FromImageApplicationProviderSpec(api.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:v1",
			})
			var testAppSpec api.ApplicationProviderSpec
			_ = testAppSpec.FromComposeApplication(testComposeApp)

			originalSpec := api.DeviceSpec{
				Os: &api.DeviceOsSpec{
					Image: "quay.io/test/os:v1.0.0",
				},
				Config:       &[]api.ConfigProviderSpec{gitConfig, inlineConfig},
				Applications: &[]api.ApplicationProviderSpec{testAppSpec},
				UpdatePolicy: &api.DeviceUpdatePolicySpec{
					DownloadSchedule: &api.UpdateSchedule{
						At:       "0 2 * * *",
						TimeZone: lo.ToPtr("UTC"),
					},
				},
			}

			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("complex-device"),
					Labels: &map[string]string{
						"environment": "test",
						"team":        "integration",
						"version":     "v1.0.0",
					},
				},
				Spec: &originalSpec,
			}

			// Store the device in database
			_, created, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			// Retrieve the device from database
			retrieved, err := devStore.Get(ctx, orgId, "complex-device")
			Expect(err).ToNot(HaveOccurred())

			// Test: Specs should be equal after database round-trip
			Expect(domain.DeviceSpecsAreEqual(originalSpec, *retrieved.Spec)).To(BeTrue(),
				"DeviceSpec should be equal after database round-trip")

			// Test: Create equivalent spec with different map ordering
			equivComposeApp := api.ComposeApplication{
				Name:    lo.ToPtr("test-app"),
				AppType: api.AppTypeCompose,
				EnvVars: &map[string]string{
					"ENV3": "value3", // Different key order
					"ENV1": "value1",
					"ENV2": "value2",
				},
			}
			_ = equivComposeApp.FromImageApplicationProviderSpec(api.ImageApplicationProviderSpec{
				Image: "quay.io/test/app:v1",
			})
			var equivAppSpec api.ApplicationProviderSpec
			_ = equivAppSpec.FromComposeApplication(equivComposeApp)

			equivalentSpec := api.DeviceSpec{
				Os: &api.DeviceOsSpec{
					Image: "quay.io/test/os:v1.0.0",
				},
				Config:       &[]api.ConfigProviderSpec{gitConfig, inlineConfig},
				Applications: &[]api.ApplicationProviderSpec{equivAppSpec},
				UpdatePolicy: &api.DeviceUpdatePolicySpec{
					DownloadSchedule: &api.UpdateSchedule{
						At:       "0 2 * * *",
						TimeZone: lo.ToPtr("UTC"),
					},
				},
			}

			// Test: Specs should be equal despite different map ordering
			Expect(domain.DeviceSpecsAreEqual(originalSpec, equivalentSpec)).To(BeTrue(),
				"DeviceSpecs should be equal despite different map key ordering")

			// Test: JSON serialization consistency
			originalJSON, err := json.Marshal(originalSpec)
			Expect(err).ToNot(HaveOccurred())

			retrievedJSON, err := json.Marshal(*retrieved.Spec)
			Expect(err).ToNot(HaveOccurred())

			var originalParsed, retrievedParsed interface{}
			err = json.Unmarshal(originalJSON, &originalParsed)
			Expect(err).ToNot(HaveOccurred())
			err = json.Unmarshal(retrievedJSON, &retrievedParsed)
			Expect(err).ToNot(HaveOccurred())

			// The normalized comparison should work
			Expect(originalParsed).To(Equal(retrievedParsed),
				"JSON-normalized DeviceSpecs should be equal")

			// Test: Different configs should not be equal
			differentConfig := api.ConfigProviderSpec{}
			err = differentConfig.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{
				Name: "different-config",
				Inline: []api.FileSpec{
					{Path: "/different.yaml", Content: "different: content"},
				},
			})
			Expect(err).ToNot(HaveOccurred())

			differentSpec := originalSpec
			differentSpec.Config = &[]api.ConfigProviderSpec{differentConfig}

			Expect(domain.DeviceSpecsAreEqual(originalSpec, differentSpec)).To(BeFalse(),
				"DeviceSpecs with different configs should not be equal")

			// Test: nil vs empty slice differences
			nilSliceSpec := originalSpec
			nilSliceSpec.Applications = nil

			emptySliceSpec := originalSpec
			emptySliceSpec.Applications = &[]api.ApplicationProviderSpec{}

			Expect(domain.DeviceSpecsAreEqual(nilSliceSpec, emptySliceSpec)).To(BeFalse(),
				"DeviceSpecs with nil vs empty slice should not be equal")
		})

		It("FleetSpec database scenarios", func() {
			// Test FleetSpecsAreEqual in realistic fleet scenarios
			fleetStore := storeInst.Fleet()

			originalFleetSpec := api.FleetSpec{
				Selector: &api.LabelSelector{
					MatchLabels: &map[string]string{
						"environment": "production",
						"team":        "backend",
						"zone":        "us-east-1",
					},
				},
				Template: struct {
					Metadata *api.ObjectMeta `json:"metadata,omitempty"`
					Spec     api.DeviceSpec  `json:"spec"`
				}{
					Metadata: &api.ObjectMeta{
						Labels: &map[string]string{
							"fleet":   "web-servers",
							"version": "v1.0.0",
							"tier":    "production",
						},
					},
					Spec: api.DeviceSpec{
						Os: &api.DeviceOsSpec{
							Image: "quay.io/fleet/web-server:v1.0.0",
						},
					},
				},
				RolloutPolicy: &api.RolloutPolicy{
					DisruptionBudget: &api.DisruptionBudget{
						MaxUnavailable: lo.ToPtr(2),
					},
				},
			}

			fleet := api.Fleet{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr("test-fleet"),
				},
				Spec: originalFleetSpec,
			}

			// Store fleet in database
			_, err := fleetStore.Create(ctx, orgId, &fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			// Retrieve fleet from database
			retrieved, err := fleetStore.Get(ctx, orgId, "test-fleet")
			Expect(err).ToNot(HaveOccurred())

			// Test: FleetSpecs should be equal after database round-trip
			Expect(domain.FleetSpecsAreEqual(originalFleetSpec, retrieved.Spec)).To(BeTrue(),
				"FleetSpec should be equal after database round-trip")

			// Test: Create equivalent spec with different map ordering
			equivalentFleetSpec := api.FleetSpec{
				Selector: &api.LabelSelector{
					MatchLabels: &map[string]string{
						"zone":        "us-east-1", // Different order
						"environment": "production",
						"team":        "backend",
					},
				},
				Template: struct {
					Metadata *api.ObjectMeta `json:"metadata,omitempty"`
					Spec     api.DeviceSpec  `json:"spec"`
				}{
					Metadata: &api.ObjectMeta{
						Labels: &map[string]string{
							"tier":    "production", // Different order
							"fleet":   "web-servers",
							"version": "v1.0.0",
						},
					},
					Spec: api.DeviceSpec{
						Os: &api.DeviceOsSpec{
							Image: "quay.io/fleet/web-server:v1.0.0",
						},
					},
				},
				RolloutPolicy: &api.RolloutPolicy{
					DisruptionBudget: &api.DisruptionBudget{
						MaxUnavailable: lo.ToPtr(2),
					},
				},
			}

			// Test: FleetSpecs should be equal despite map ordering differences
			Expect(domain.FleetSpecsAreEqual(originalFleetSpec, equivalentFleetSpec)).To(BeTrue(),
				"FleetSpecs should be equal despite different map key ordering")

			// Test: Different rollout policies should not be equal
			differentFleetSpec := originalFleetSpec
			differentFleetSpec.RolloutPolicy = &api.RolloutPolicy{
				DisruptionBudget: &api.DisruptionBudget{
					MaxUnavailable: lo.ToPtr(1), // Different value
				},
			}

			Expect(domain.FleetSpecsAreEqual(originalFleetSpec, differentFleetSpec)).To(BeFalse(),
				"FleetSpecs with different rollout policies should not be equal")
		})

		It("CountByOrgAndStatus", func() {
			// Create a few devices with fleet assignments
			testutil.CreateTestDevice(ctx, devStore, orgId, "fleet-device-1", lo.ToPtr("Fleet/test-fleet"), nil, nil)
			testutil.CreateTestDevice(ctx, devStore, orgId, "fleet-device-2", lo.ToPtr("Fleet/test-fleet"), nil, nil)

			// Test CountByOrgAndStatus with groupByFleet=true
			results, err := devStore.CountByOrgAndStatus(ctx, &orgId, store.DeviceStatusTypeSummary, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).ToNot(BeEmpty())

			// Verify we get results grouped by fleet
			fleetCounts := make(map[string]int64)
			for _, result := range results {
				Expect(result.OrgID).To(Equal(orgId.String()))
				fleetCounts[result.Fleet] += result.Count
			}

			// Should have devices in both "" fleet (original 3 devices with nil owner) and "Fleet/test-fleet" (2 new devices)
			Expect(fleetCounts[""]).To(Equal(int64(3)))                 // mydevice-1,2,3 (no fleet assignment = empty string)
			Expect(fleetCounts["Fleet/test-fleet"]).To(Equal(int64(2))) // fleet-device-1,2

			// Test CountByOrgAndStatus with groupByFleet=false
			results, err = devStore.CountByOrgAndStatus(ctx, &orgId, store.DeviceStatusTypeSummary, false)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).ToNot(BeEmpty())

			// All devices should be aggregated without fleet grouping (Fleet field will be empty)
			totalCount := int64(0)
			for _, result := range results {
				Expect(result.Fleet).To(Equal("")) // No fleet field selected when groupByFleet=false
				totalCount += result.Count
			}
			Expect(totalCount).To(Equal(int64(5))) // All 5 devices

			// Test different status types
			results, err = devStore.CountByOrgAndStatus(ctx, &orgId, store.DeviceStatusTypeApplication, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).ToNot(BeEmpty())

			results, err = devStore.CountByOrgAndStatus(ctx, &orgId, store.DeviceStatusTypeUpdate, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).ToNot(BeEmpty())

			// Test with nil orgId
			results, err = devStore.CountByOrgAndStatus(ctx, nil, store.DeviceStatusTypeSummary, true)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).ToNot(BeEmpty())

			// Verify organization ID is included in results
			for _, result := range results {
				Expect(result.OrgID).To(Equal(orgId.String()))
			}
		})

		It("PrepareDevicesAfterRestore sets annotation, clears lastSeen, and sets status", func() {
			// Create a fresh test device to avoid resource version conflicts
			testDeviceName := "restore-test-device"
			testDevice := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(testDeviceName),
					Annotations: &map[string]string{
						"existing-annotation": "existing-value",
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusOnline,
						Info:   lo.ToPtr("Device is online"),
					},
					Conditions:   []api.Condition{},
					Applications: []api.DeviceApplicationStatus{},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusUnknown,
					},
					Config: api.DeviceConfigStatus{
						RenderedVersion: "test-version",
					},
					Integrity: api.DeviceIntegrityStatus{
						Status: api.DeviceIntegrityStatusUnknown,
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusUnknown,
						Disk:   api.DeviceResourceStatusUnknown,
						Memory: api.DeviceResourceStatusUnknown,
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUnknown,
					},
					Lifecycle: api.DeviceLifecycleStatus{
						Status: api.DeviceLifecycleStatusUnknown,
					},
				},
			}

			// Create the test device (using fromAPI: false to preserve annotations for testing)
			createdDevice, created, err := devStore.CreateOrUpdate(ctx, orgId, testDevice, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(createdDevice).ToNot(BeNil())
			Expect(created).To(BeTrue())

			// Verify initial state
			Expect(createdDevice.Status.LastSeen.IsZero()).To(BeFalse())
			Expect(createdDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))

			// Execute: Run PrepareDevicesAfterRestore
			devicesUpdated, err := devStore.PrepareDevicesAfterRestore(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(devicesUpdated).To(BeNumerically(">=", int64(1))) // Should update at least our test device

			// Verify: Check that the test device has been updated correctly
			device, err := devStore.Get(ctx, orgId, testDeviceName)
			Expect(err).ToNot(HaveOccurred())

			// Check that restoration annotation was added
			Expect(device.Metadata.Annotations).ToNot(BeNil())
			annotations := *device.Metadata.Annotations
			Expect(annotations[api.DeviceAnnotationAwaitingReconnect]).To(Equal("true"))

			// Check that existing annotation was preserved
			Expect(annotations["existing-annotation"]).To(Equal("existing-value"))

			// Check that lastSeen was cleared (should be nil)
			Expect(device.Status).ToNot(BeNil())
			Expect(device.Status.LastSeen).To(BeNil())

			// Check that status summary was set to waiting for connection
			Expect(device.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			Expect(device.Status.Summary.Info).ToNot(BeNil())
			Expect(*device.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))

			// Check that updated status was set to unknown
			Expect(device.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusUnknown))

			// Check that other status fields were preserved
			Expect(device.Status.Config.RenderedVersion).To(Equal("test-version"))

			// Check that last_seen column was cleared using GetLastSeen method
			lastSeen, err := devStore.GetLastSeen(ctx, orgId, testDeviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(lastSeen).To(BeNil(), "last_seen column should be cleared after PrepareDevicesAfterRestore")
		})

		It("PrepareDevicesAfterRestore handles devices with no existing status", func() {
			// Create a device with no status
			deviceName := "test-device-no-status"
			device := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: nil, // No status
			}

			_, created, err := devStore.CreateOrUpdate(ctx, orgId, &device, nil, true, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			// Execute: Run PrepareDevicesAfterRestore
			devicesUpdated, err := devStore.PrepareDevicesAfterRestore(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(devicesUpdated).To(BeNumerically(">=", int64(1))) // Should update at least our new device

			// Verify: Check that the device was updated correctly
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())

			// Check that restoration annotation was added
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			annotations := *updatedDevice.Metadata.Annotations
			Expect(annotations[api.DeviceAnnotationAwaitingReconnect]).To(Equal("true"))

			// Check that status was created with proper summary
			Expect(updatedDevice.Status).ToNot(BeNil())
			Expect(updatedDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect))
			Expect(updatedDevice.Status.Summary.Info).ToNot(BeNil())
			Expect(*updatedDevice.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))

			// Check that updated status was set to unknown
			Expect(updatedDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusUnknown))

			// Check that lastSeen is nil (not set)
			Expect(updatedDevice.Status.LastSeen).To(BeNil())
		})

		It("PrepareDevicesAfterRestore excludes decommissioned and decommissioning devices", func() {
			// Create a decommissioning device
			decommissioningDeviceName := "decommissioning-device"
			decommissioningDevice := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(decommissioningDeviceName),
					Annotations: &map[string]string{
						"existing-annotation": "existing-value",
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
					Decommissioning: &api.DeviceDecommission{
						Target: api.DeviceDecommissionTargetTypeUnenroll,
					},
				},
				Status: &api.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusOnline,
						Info:   lo.ToPtr("Device is online"),
					},
					Lifecycle: api.DeviceLifecycleStatus{
						Status: api.DeviceLifecycleStatusDecommissioning,
					},
					Conditions:   []api.Condition{},
					Applications: []api.DeviceApplicationStatus{},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusUnknown,
					},
					Config: api.DeviceConfigStatus{
						RenderedVersion: "test-version",
					},
					Integrity: api.DeviceIntegrityStatus{
						Status: api.DeviceIntegrityStatusUnknown,
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusUnknown,
						Disk:   api.DeviceResourceStatusUnknown,
						Memory: api.DeviceResourceStatusUnknown,
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUnknown,
					},
				},
			}

			// Create a decommissioned device
			decommissionedDeviceName := "decommissioned-device"
			decommissionedDevice := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(decommissionedDeviceName),
					Annotations: &map[string]string{
						"existing-annotation": "existing-value",
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
					Decommissioning: &api.DeviceDecommission{
						Target: api.DeviceDecommissionTargetTypeUnenroll,
					},
				},
				Status: &api.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusOnline,
						Info:   lo.ToPtr("Device is online"),
					},
					Lifecycle: api.DeviceLifecycleStatus{
						Status: api.DeviceLifecycleStatusDecommissioned,
					},
					Conditions:   []api.Condition{},
					Applications: []api.DeviceApplicationStatus{},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusUnknown,
					},
					Config: api.DeviceConfigStatus{
						RenderedVersion: "test-version",
					},
					Integrity: api.DeviceIntegrityStatus{
						Status: api.DeviceIntegrityStatusUnknown,
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusUnknown,
						Disk:   api.DeviceResourceStatusUnknown,
						Memory: api.DeviceResourceStatusUnknown,
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUnknown,
					},
				},
			}

			// Create a normal device that should be updated
			normalDeviceName := "normal-device"
			normalDevice := api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(normalDeviceName),
					Annotations: &map[string]string{
						"existing-annotation": "existing-value",
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusOnline,
						Info:   lo.ToPtr("Device is online"),
					},
					Lifecycle: api.DeviceLifecycleStatus{
						Status: api.DeviceLifecycleStatusEnrolled,
					},
					Conditions:   []api.Condition{},
					Applications: []api.DeviceApplicationStatus{},
					ApplicationsSummary: api.DeviceApplicationsSummaryStatus{
						Status: api.ApplicationsSummaryStatusUnknown,
					},
					Config: api.DeviceConfigStatus{
						RenderedVersion: "test-version",
					},
					Integrity: api.DeviceIntegrityStatus{
						Status: api.DeviceIntegrityStatusUnknown,
					},
					Resources: api.DeviceResourceStatus{
						Cpu:    api.DeviceResourceStatusUnknown,
						Disk:   api.DeviceResourceStatusUnknown,
						Memory: api.DeviceResourceStatusUnknown,
					},
					Updated: api.DeviceUpdatedStatus{
						Status: api.DeviceUpdatedStatusUnknown,
					},
				},
			}

			// Create all devices
			_, created, err := devStore.CreateOrUpdate(ctx, orgId, &decommissioningDevice, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			setLastSeen(db, orgId, decommissioningDeviceName, *decommissioningDevice.Status.LastSeen)

			_, created, err = devStore.CreateOrUpdate(ctx, orgId, &decommissionedDevice, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			setLastSeen(db, orgId, decommissionedDeviceName, *decommissionedDevice.Status.LastSeen)

			_, created, err = devStore.CreateOrUpdate(ctx, orgId, &normalDevice, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			setLastSeen(db, orgId, normalDeviceName, *normalDevice.Status.LastSeen)

			// Execute: Run PrepareDevicesAfterRestore
			devicesUpdated, err := devStore.PrepareDevicesAfterRestore(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(devicesUpdated).To(BeNumerically(">=", int64(1))) // Should update at least the normal device

			// Verify: Check that decommissioning device was NOT updated
			decommissioningDeviceAfter, err := devStore.Get(ctx, orgId, decommissioningDeviceName)
			Expect(err).ToNot(HaveOccurred())

			// Should NOT have the awaitingReconnect annotation
			Expect(decommissioningDeviceAfter.Metadata.Annotations).ToNot(BeNil())
			annotations := *decommissioningDeviceAfter.Metadata.Annotations
			Expect(annotations[api.DeviceAnnotationAwaitingReconnect]).To(BeEmpty(), "Decommissioning device should NOT have awaitingReconnect annotation")

			// Should preserve existing annotation
			Expect(annotations["existing-annotation"]).To(Equal("existing-value"))

			// Should NOT have lastSeen cleared
			decommissioningLastSeen, err := devStore.GetLastSeen(ctx, orgId, decommissioningDeviceName)
			Expect(err).ToNot(HaveOccurred())

			Expect(decommissioningLastSeen).ToNot(BeNil())
			Expect(decommissioningLastSeen.IsZero()).To(BeFalse(), "Decommissioning device should NOT have lastSeen cleared")

			// Should NOT have status summary changed
			Expect(decommissioningDeviceAfter.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline), "Decommissioning device should NOT have status summary changed")

			// Verify: Check that decommissioned device was NOT updated
			decommissionedDeviceAfter, err := devStore.Get(ctx, orgId, decommissionedDeviceName)
			Expect(err).ToNot(HaveOccurred())

			// Should NOT have the awaitingReconnect annotation
			Expect(decommissionedDeviceAfter.Metadata.Annotations).ToNot(BeNil())
			annotations = *decommissionedDeviceAfter.Metadata.Annotations
			Expect(annotations[api.DeviceAnnotationAwaitingReconnect]).To(BeEmpty(), "Decommissioned device should NOT have awaitingReconnect annotation")

			// Should preserve existing annotation
			Expect(annotations["existing-annotation"]).To(Equal("existing-value"))

			// Should NOT have lastSeen cleared
			decommissionedLastSeen, err := devStore.GetLastSeen(ctx, orgId, decommissionedDeviceName)
			Expect(err).ToNot(HaveOccurred())

			Expect(decommissionedLastSeen).ToNot(BeNil())
			Expect(decommissionedLastSeen.IsZero()).To(BeFalse(), "Decommissioned device should NOT have lastSeen cleared")

			// Should NOT have status summary changed
			Expect(decommissionedDeviceAfter.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline), "Decommissioned device should NOT have status summary changed")

			// Verify: Check that normal device WAS updated
			normalDeviceAfter, err := devStore.Get(ctx, orgId, normalDeviceName)
			Expect(err).ToNot(HaveOccurred())

			// Should have the awaitingReconnect annotation
			Expect(normalDeviceAfter.Metadata.Annotations).ToNot(BeNil())
			annotations = *normalDeviceAfter.Metadata.Annotations
			Expect(annotations[api.DeviceAnnotationAwaitingReconnect]).To(Equal("true"), "Normal device SHOULD have awaitingReconnect annotation")

			// Should preserve existing annotation
			Expect(annotations["existing-annotation"]).To(Equal("existing-value"))

			// Should have lastSeen cleared
			normalLastSeen, err := devStore.GetLastSeen(ctx, orgId, normalDeviceName)
			Expect(err).ToNot(HaveOccurred())

			Expect(normalLastSeen).To(BeNil(), "Normal device SHOULD have lastSeen cleared")

			// Should have status summary changed to awaiting reconnect
			Expect(normalDeviceAfter.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusAwaitingReconnect), "Normal device SHOULD have status summary changed to AwaitingReconnect")
			Expect(normalDeviceAfter.Status.Summary.Info).ToNot(BeNil())
			Expect(*normalDeviceAfter.Status.Summary.Info).To(Equal("Device has not reconnected since restore to confirm its current state."))
		})

		It("PrepareDevicesAfterRestore properly clears last_seen column", func() {
			// Create a test device with last_seen set
			deviceName := "last-seen-column-test"
			device := &api.Device{
				Metadata: api.ObjectMeta{Name: lo.ToPtr(deviceName)},
				Spec:     &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "test-image"}},
				Status: &api.DeviceStatus{
					LastSeen: lo.ToPtr(time.Now()),
					Summary:  api.DeviceSummaryStatus{Status: api.DeviceSummaryStatusOnline},
				},
			}

			// Create the device
			_, created, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			setLastSeen(db, orgId, deviceName, *device.Status.LastSeen)

			// Verify initial state - last_seen column should have a value
			lastSeenBefore, err := devStore.GetLastSeen(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(lastSeenBefore).ToNot(BeNil(), "last_seen column should have a value initially")

			// Execute: Run PrepareDevicesAfterRestore
			devicesUpdated, err := devStore.PrepareDevicesAfterRestore(ctx)
			Expect(err).ToNot(HaveOccurred())
			Expect(devicesUpdated).To(BeNumerically(">=", int64(1)))

			// Verify: Check that last_seen column was cleared
			lastSeenAfter, err := devStore.GetLastSeen(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(lastSeenAfter).To(BeNil(), "last_seen column should be cleared after PrepareDevicesAfterRestore")
		})
	})

	Context("Healthcheck", func() {
		It("should update last_seen column for specified devices", func() {
			// Create test devices
			device1Name := "healthcheck-device-1"
			device2Name := "healthcheck-device-2"
			device3Name := "healthcheck-device-3"

			devices := []*api.Device{
				{
					Metadata: api.ObjectMeta{Name: lo.ToPtr(device1Name)},
					Spec:     &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "test-image"}},
					Status: &api.DeviceStatus{
						LastSeen: lo.ToPtr(time.Now().Add(-1 * time.Hour)), // Old timestamp
						Summary:  api.DeviceSummaryStatus{Status: api.DeviceSummaryStatusOnline},
					},
				},
				{
					Metadata: api.ObjectMeta{Name: lo.ToPtr(device2Name)},
					Spec:     &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "test-image"}},
					Status: &api.DeviceStatus{
						LastSeen: lo.ToPtr(time.Now().Add(-2 * time.Hour)), // Old timestamp
						Summary:  api.DeviceSummaryStatus{Status: api.DeviceSummaryStatusOnline},
					},
				},
				{
					Metadata: api.ObjectMeta{Name: lo.ToPtr(device3Name)},
					Spec:     &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "test-image"}},
					Status: &api.DeviceStatus{
						LastSeen: lo.ToPtr(time.Now().Add(-3 * time.Hour)), // Old timestamp
						Summary:  api.DeviceSummaryStatus{Status: api.DeviceSummaryStatusOnline},
					},
				},
			}

			// Create the devices
			for _, device := range devices {
				_, created, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
				Expect(err).ToNot(HaveOccurred())
				Expect(created).To(BeTrue())
				setLastSeen(db, orgId, *device.Metadata.Name, *device.Status.LastSeen)
			}

			// Record initial last_seen values
			initialLastSeen := make(map[string]*time.Time)
			for _, deviceName := range []string{device1Name, device2Name, device3Name} {
				lastSeen, err := devStore.GetLastSeen(ctx, orgId, deviceName)
				Expect(err).ToNot(HaveOccurred())
				initialLastSeen[deviceName] = lastSeen
			}

			// Wait a bit to ensure timestamp difference
			time.Sleep(100 * time.Millisecond)

			// Execute: Run healthcheck on devices 1 and 2 only
			deviceNames := []string{device1Name, device2Name}
			err := devStore.Healthcheck(ctx, orgId, deviceNames)
			Expect(err).ToNot(HaveOccurred())

			// Verify: Check that devices 1 and 2 have updated last_seen
			for _, deviceName := range deviceNames {
				lastSeen, err := devStore.GetLastSeen(ctx, orgId, deviceName)
				Expect(err).ToNot(HaveOccurred())

				// last_seen should be updated (newer than initial)
				Expect(lastSeen).ToNot(BeNil())
				Expect(lastSeen.After(*initialLastSeen[deviceName])).To(BeTrue(),
					"Device %s should have updated last_seen", deviceName)
			}

			// Verify: Check that device 3 was NOT updated
			lastSeen, err := devStore.GetLastSeen(ctx, orgId, device3Name)
			Expect(err).ToNot(HaveOccurred())
			Expect(lastSeen).To(Equal(initialLastSeen[device3Name]),
				"Device 3 should NOT have updated last_seen")
		})

		It("should handle empty device list gracefully", func() {
			// Execute: Run healthcheck with empty list
			err := devStore.Healthcheck(ctx, orgId, []string{})
			Expect(err).ToNot(HaveOccurred())
		})

		It("should handle non-existent devices gracefully", func() {
			// Execute: Run healthcheck on non-existent devices
			err := devStore.Healthcheck(ctx, orgId, []string{"non-existent-device-1", "non-existent-device-2"})
			Expect(err).ToNot(HaveOccurred())
		})

		It("should update last_seen without affecting resource_version", func() {
			// Create a test device
			deviceName := "healthcheck-last-seen-test"
			device := &api.Device{
				Metadata: api.ObjectMeta{Name: lo.ToPtr(deviceName)},
				Spec:     &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "test-image"}},
				Status: &api.DeviceStatus{
					Summary: api.DeviceSummaryStatus{Status: api.DeviceSummaryStatusOnline},
				},
			}

			// Create the device
			createdDevice, created, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())

			// Execute: Run healthcheck
			err = devStore.Healthcheck(ctx, orgId, []string{deviceName})
			Expect(err).ToNot(HaveOccurred())

			// Record initial resource version and last_seen
			initialResourceVersion := *createdDevice.Metadata.ResourceVersion
			initialLastSeen, err := devStore.GetLastSeen(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(initialLastSeen).ToNot(BeNil())

			// Wait a bit to ensure timestamp difference
			time.Sleep(100 * time.Millisecond)

			// Execute: Run healthcheck
			err = devStore.Healthcheck(ctx, orgId, []string{deviceName})
			Expect(err).ToNot(HaveOccurred())

			// Verify: Check that resource version was NOT changed
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(*updatedDevice.Metadata.ResourceVersion).To(Equal(initialResourceVersion), "resource version should NOT be changed by healthcheck")

			// Verify: Check that last_seen was updated
			updatedLastSeen, err := devStore.GetLastSeen(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedLastSeen).ToNot(BeNil())
			Expect(updatedLastSeen.After(*initialLastSeen)).To(BeTrue(), "last_seen should be updated by healthcheck")
		})

		It("should handle mixed existing and non-existent devices", func() {
			// Create one test device
			deviceName := "healthcheck-mixed-test"
			device := &api.Device{
				Metadata: api.ObjectMeta{Name: lo.ToPtr(deviceName)},
				Spec:     &api.DeviceSpec{Os: &api.DeviceOsSpec{Image: "test-image"}},
				Status: &api.DeviceStatus{
					Summary: api.DeviceSummaryStatus{Status: api.DeviceSummaryStatusOnline},
				},
			}

			// Create the device
			_, created, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())
			Expect(created).To(BeTrue())
			initialLastSeen := time.Now().Add(-1 * time.Hour)
			setLastSeen(db, orgId, deviceName, initialLastSeen)

			// Wait a bit to ensure timestamp difference
			time.Sleep(100 * time.Millisecond)

			// Execute: Run healthcheck on mix of existing and non-existent devices
			deviceNames := []string{deviceName, "non-existent-device-1", "non-existent-device-2"}
			err = devStore.Healthcheck(ctx, orgId, deviceNames)
			Expect(err).ToNot(HaveOccurred())

			// Verify: Check that the existing device was updated
			updatedLastSeen, err := devStore.GetLastSeen(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedLastSeen).ToNot(BeNil())
			Expect(updatedLastSeen.After(initialLastSeen)).To(BeTrue())
		})
	})

	Context("ProcessAwaitingReconnectAnnotation", func() {
		It("should return false when device has no awaiting reconnect annotation", func() {
			// Create a device without awaiting reconnect annotation
			deviceName := "no-awaiting-reconnect-device"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
			}

			_, _, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Process awaiting reconnect annotation - should return false
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(wasConflictPaused).To(BeFalse())

			// Verify device is unchanged
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			Expect(len(*updatedDevice.Metadata.Annotations)).To(Equal(0))
		})

		It("should return false when awaiting reconnect annotation is not 'true'", func() {
			// Create a device with awaiting reconnect annotation set to 'false'
			deviceName := "awaiting-reconnect-false-device"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "false",
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
			}

			_, _, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Process awaiting reconnect annotation - should return false
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(wasConflictPaused).To(BeFalse())

			// Verify device is unchanged
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			Expect((*updatedDevice.Metadata.Annotations)[api.DeviceAnnotationAwaitingReconnect]).To(Equal("false"))
		})

		It("should remove awaiting reconnect annotation and set normal status when device version <= service version", func() {
			// Create a device with awaiting reconnect annotation and service version
			deviceName := "normal-reconnect-device"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "true",
						api.DeviceAnnotationRenderedVersion:   "5", // Service version
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusAwaitingReconnect,
					},
				},
			}

			_, _, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Process awaiting reconnect annotation with device version <= service version
			deviceReportedVersion := "3" // Lower than service version 5
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, &deviceReportedVersion)
			Expect(err).ToNot(HaveOccurred())
			Expect(wasConflictPaused).To(BeFalse())

			// Verify awaiting reconnect annotation was removed
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			_, hasAwaitingReconnect := (*updatedDevice.Metadata.Annotations)[api.DeviceAnnotationAwaitingReconnect]
			Expect(hasAwaitingReconnect).To(BeFalse())

			// Verify conflict paused annotation was not added
			_, hasConflictPaused := (*updatedDevice.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
			Expect(hasConflictPaused).To(BeFalse())

			// Verify status was updated to normal
			Expect(updatedDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))
			Expect(updatedDevice.Status.Summary.Info).ToNot(BeNil())
			Expect(*updatedDevice.Status.Summary.Info).To(Equal("Device is up to date"))

			// Verify updated status was set to OutOfDate (device version 3 < service version 5)
			Expect(updatedDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusOutOfDate))

			// Verify status.config.renderedVersion was updated to device reported version
			Expect(updatedDevice.Status.Config.RenderedVersion).To(Equal(deviceReportedVersion))

			// Verify resource version was incremented
			Expect(updatedDevice.Metadata.ResourceVersion).ToNot(BeNil())
		})

		It("should remove awaiting reconnect annotation and add conflict paused when device version > service version", func() {
			// Create a device with awaiting reconnect annotation and service version
			deviceName := "conflict-paused-device"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "true",
						api.DeviceAnnotationRenderedVersion:   "3", // Service version
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusAwaitingReconnect,
					},
				},
			}

			_, _, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Process awaiting reconnect annotation with device version > service version
			deviceReportedVersion := "5" // Higher than service version 3
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, &deviceReportedVersion)
			Expect(err).ToNot(HaveOccurred())
			Expect(wasConflictPaused).To(BeTrue())

			// Verify awaiting reconnect annotation was removed
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			_, hasAwaitingReconnect := (*updatedDevice.Metadata.Annotations)[api.DeviceAnnotationAwaitingReconnect]
			Expect(hasAwaitingReconnect).To(BeFalse())

			// Verify conflict paused annotation was added
			conflictPausedValue, hasConflictPaused := (*updatedDevice.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
			Expect(hasConflictPaused).To(BeTrue())
			Expect(conflictPausedValue).To(Equal("true"))

			// Verify status was updated to conflict paused with detailed info
			Expect(updatedDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusConflictPaused))
			Expect(updatedDevice.Status.Summary.Info).ToNot(BeNil())
			Expect(*updatedDevice.Status.Summary.Info).To(ContainSubstring("Device reconciliation is paused due to a state conflict"))
			Expect(*updatedDevice.Status.Summary.Info).To(ContainSubstring("device reported version 5 > device version known to service 3"))

			// Verify updated status was set to OutOfDate (device version 5 > service version 3)
			Expect(updatedDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusOutOfDate))

			// Verify status.config.renderedVersion was updated to device reported version
			Expect(updatedDevice.Status.Config.RenderedVersion).To(Equal(deviceReportedVersion))

			// Verify resource version was incremented
			Expect(updatedDevice.Metadata.ResourceVersion).ToNot(BeNil())
		})

		It("should handle nil device reported version gracefully", func() {
			// Create a device with awaiting reconnect annotation and service version
			deviceName := "nil-version-device"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "true",
						api.DeviceAnnotationRenderedVersion:   "5", // Service version
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusAwaitingReconnect,
					},
				},
			}

			_, _, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Process awaiting reconnect annotation with nil device reported version
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, nil)
			Expect(err).ToNot(HaveOccurred())
			Expect(wasConflictPaused).To(BeFalse()) // Should be false since device version (0) <= service version (5)

			// Verify awaiting reconnect annotation was removed
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			_, hasAwaitingReconnect := (*updatedDevice.Metadata.Annotations)[api.DeviceAnnotationAwaitingReconnect]
			Expect(hasAwaitingReconnect).To(BeFalse())

			// Verify status was updated to normal
			Expect(updatedDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))

			// Verify updated status was set to OutOfDate (device version 0 < service version 5)
			Expect(updatedDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusOutOfDate))

			// Verify status.config.renderedVersion was updated to "0" (default for nil)
			Expect(updatedDevice.Status.Config.RenderedVersion).To(Equal("0"))
		})

		It("should handle empty device reported version gracefully", func() {
			// Create a device with awaiting reconnect annotation and service version
			deviceName := "empty-version-device"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "true",
						api.DeviceAnnotationRenderedVersion:   "5", // Service version
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusAwaitingReconnect,
					},
				},
			}

			_, _, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Process awaiting reconnect annotation with empty device reported version
			emptyVersion := ""
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, &emptyVersion)
			Expect(err).ToNot(HaveOccurred())
			Expect(wasConflictPaused).To(BeFalse()) // Should be false since device version (0) <= service version (5)

			// Verify awaiting reconnect annotation was removed
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			_, hasAwaitingReconnect := (*updatedDevice.Metadata.Annotations)[api.DeviceAnnotationAwaitingReconnect]
			Expect(hasAwaitingReconnect).To(BeFalse())

			// Verify status was updated to normal
			Expect(updatedDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))

			// Verify updated status was set to OutOfDate (device version 0 < service version 5)
			Expect(updatedDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusOutOfDate))

			// Verify status.config.renderedVersion was updated to "0" (default for empty)
			Expect(updatedDevice.Status.Config.RenderedVersion).To(Equal("0"))
		})

		It("should handle invalid device reported version gracefully", func() {
			// Create a device with awaiting reconnect annotation and service version
			deviceName := "invalid-version-device"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "true",
						api.DeviceAnnotationRenderedVersion:   "5", // Service version
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusAwaitingReconnect,
					},
				},
			}

			_, _, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Process awaiting reconnect annotation with invalid device reported version
			invalidVersion := "not-a-number"
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, &invalidVersion)
			Expect(err).ToNot(HaveOccurred())
			Expect(wasConflictPaused).To(BeFalse()) // Should be false since device version (0) <= service version (5)

			// Verify awaiting reconnect annotation was removed
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			_, hasAwaitingReconnect := (*updatedDevice.Metadata.Annotations)[api.DeviceAnnotationAwaitingReconnect]
			Expect(hasAwaitingReconnect).To(BeFalse())

			// Verify status was updated to normal
			Expect(updatedDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusOnline))

			// Verify updated status was set to OutOfDate (device version 0 < service version 5)
			Expect(updatedDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusOutOfDate))

			// Verify status.config.renderedVersion was updated to the original invalid value
			Expect(updatedDevice.Status.Config.RenderedVersion).To(Equal("not-a-number"))
		})

		It("should handle missing service rendered version gracefully", func() {
			// Create a device with awaiting reconnect annotation but no service version
			deviceName := "no-service-version-device"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "true",
						// No DeviceAnnotationRenderedVersion
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusAwaitingReconnect,
					},
				},
			}

			_, _, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Process awaiting reconnect annotation
			deviceReportedVersion := "5"
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, &deviceReportedVersion)
			Expect(err).ToNot(HaveOccurred())
			Expect(wasConflictPaused).To(BeTrue()) // Should be true since device version (5) > service version (0)

			// Verify conflict paused annotation was added
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			conflictPausedValue, hasConflictPaused := (*updatedDevice.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
			Expect(hasConflictPaused).To(BeTrue())
			Expect(conflictPausedValue).To(Equal("true"))

			// Verify status was updated to conflict paused
			Expect(updatedDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusConflictPaused))
			Expect(updatedDevice.Status.Summary.Info).ToNot(BeNil())
			Expect(*updatedDevice.Status.Summary.Info).To(ContainSubstring("device reported version 5 > device version known to service 0"))

			// Verify updated status was set to OutOfDate (device version 5 > service version 0)
			Expect(updatedDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusOutOfDate))

			// Verify status.config.renderedVersion was updated to device reported version
			Expect(updatedDevice.Status.Config.RenderedVersion).To(Equal(deviceReportedVersion))
		})

		It("should handle invalid service rendered version gracefully", func() {
			// Create a device with awaiting reconnect annotation and invalid service version
			deviceName := "invalid-service-version-device"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "true",
						api.DeviceAnnotationRenderedVersion:   "not-a-number", // Invalid service version
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusAwaitingReconnect,
					},
				},
			}

			_, _, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Process awaiting reconnect annotation
			deviceReportedVersion := "5"
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, &deviceReportedVersion)
			Expect(err).ToNot(HaveOccurred())
			Expect(wasConflictPaused).To(BeTrue()) // Should be true since device version (5) > service version (0)

			// Verify conflict paused annotation was added
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			conflictPausedValue, hasConflictPaused := (*updatedDevice.Metadata.Annotations)[api.DeviceAnnotationConflictPaused]
			Expect(hasConflictPaused).To(BeTrue())
			Expect(conflictPausedValue).To(Equal("true"))

			// Verify status was updated to conflict paused
			Expect(updatedDevice.Status.Summary.Status).To(Equal(api.DeviceSummaryStatusConflictPaused))
			Expect(updatedDevice.Status.Summary.Info).ToNot(BeNil())
			Expect(*updatedDevice.Status.Summary.Info).To(ContainSubstring("device reported version 5 > device version known to service 0"))

			// Verify updated status was set to OutOfDate (device version 5 > service version 0)
			Expect(updatedDevice.Status.Updated.Status).To(Equal(api.DeviceUpdatedStatusOutOfDate))

			// Verify status.config.renderedVersion was updated to device reported version
			Expect(updatedDevice.Status.Config.RenderedVersion).To(Equal(deviceReportedVersion))
		})

		It("should return error when device does not exist", func() {
			// Try to process awaiting reconnect annotation for non-existent device
			deviceName := "non-existent-device"
			deviceReportedVersion := "5"
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, &deviceReportedVersion)
			Expect(err).To(HaveOccurred())
			Expect(wasConflictPaused).To(BeFalse())
		})

		It("should preserve existing annotations when processing", func() {
			// Create a device with awaiting reconnect annotation and other annotations
			deviceName := "preserve-annotations-device"
			device := &api.Device{
				Metadata: api.ObjectMeta{
					Name: lo.ToPtr(deviceName),
					Annotations: &map[string]string{
						api.DeviceAnnotationAwaitingReconnect: "true",
						api.DeviceAnnotationRenderedVersion:   "3",
						"custom-annotation":                   "custom-value",
						"another-annotation":                  "another-value",
					},
				},
				Spec: &api.DeviceSpec{
					Os: &api.DeviceOsSpec{Image: "test-image"},
				},
				Status: &api.DeviceStatus{
					Summary: api.DeviceSummaryStatus{
						Status: api.DeviceSummaryStatusAwaitingReconnect,
					},
				},
			}

			_, _, err := devStore.CreateOrUpdate(ctx, orgId, device, nil, false, nil, callback)
			Expect(err).ToNot(HaveOccurred())

			// Process awaiting reconnect annotation
			deviceReportedVersion := "5"
			wasConflictPaused, err := devStore.ProcessAwaitingReconnectAnnotation(ctx, orgId, deviceName, &deviceReportedVersion)
			Expect(err).ToNot(HaveOccurred())
			Expect(wasConflictPaused).To(BeTrue())

			// Verify awaiting reconnect annotation was removed but others preserved
			updatedDevice, err := devStore.Get(ctx, orgId, deviceName)
			Expect(err).ToNot(HaveOccurred())
			Expect(updatedDevice.Metadata.Annotations).ToNot(BeNil())
			annotations := *updatedDevice.Metadata.Annotations

			// Awaiting reconnect should be removed
			_, hasAwaitingReconnect := annotations[api.DeviceAnnotationAwaitingReconnect]
			Expect(hasAwaitingReconnect).To(BeFalse())

			// Conflict paused should be added
			Expect(annotations[api.DeviceAnnotationConflictPaused]).To(Equal("true"))

			// Other annotations should be preserved
			Expect(annotations["custom-annotation"]).To(Equal("custom-value"))
			Expect(annotations["another-annotation"]).To(Equal("another-value"))
			Expect(annotations[api.DeviceAnnotationRenderedVersion]).To(Equal("3"))

			// Verify status.config.renderedVersion was updated to device reported version
			Expect(updatedDevice.Status.Config.RenderedVersion).To(Equal(deviceReportedVersion))
		})
	})
})

func createTestConfigProvider(contents string) (string, error) {
	provider := api.ConfigProviderSpec{}
	files := []api.FileSpec{
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

func setLastSeen(db *gorm.DB, orgId uuid.UUID, name string, lastSeen time.Time) {
	Expect(db.Model(&model.DeviceTimestamp{}).Where("org_id = ? AND name = ?", orgId, name).
		Update("last_seen", lastSeen).Error).ToNot(HaveOccurred())
}
