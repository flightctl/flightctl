package store_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

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

				imageApp := &api.ImageApplicationProviderSpec{
					Image:   "quay.io/flightctl/test-app:latest",
					Volumes: &[]api.ApplicationVolume{imageVolume},
				}
				imageAppItem := api.ApplicationProviderSpec{
					AppType: lo.ToPtr(api.AppTypeCompose),
					Name:    lo.ToPtr("test-image-app"),
				}
				_ = imageAppItem.FromImageApplicationProviderSpec(*imageApp)

				inlineApp := &api.InlineApplicationProviderSpec{
					Inline: []api.ApplicationContent{
						{
							Path:    "docker-compose.yaml",
							Content: lo.ToPtr("version: '3'\nservices:\n  test:\n    image: alpine\n"),
						},
					},
					Volumes: &[]api.ApplicationVolume{imageVolume}, // Reuse the same volume
				}
				inlineAppItem := api.ApplicationProviderSpec{
					AppType: lo.ToPtr(api.AppTypeCompose),
					Name:    lo.ToPtr("test-inline-app"),
				}
				_ = inlineAppItem.FromInlineApplicationProviderSpec(*inlineApp)

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

			// Set first rendered config
			err = devStore.UpdateRendered(ctx, orgId, "dev", firstConfig, "")
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

			originalSpec := api.DeviceSpec{
				Os: &api.DeviceOsSpec{
					Image: "quay.io/test/os:v1.0.0",
				},
				Config: &[]api.ConfigProviderSpec{gitConfig, inlineConfig},
				Applications: &[]api.ApplicationProviderSpec{
					{
						Name:    lo.ToPtr("test-app"),
						AppType: lo.ToPtr(api.AppTypeCompose),
						EnvVars: &map[string]string{
							"ENV1": "value1",
							"ENV2": "value2",
							"ENV3": "value3",
						},
					},
				},
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
			Expect(api.DeviceSpecsAreEqual(originalSpec, *retrieved.Spec)).To(BeTrue(),
				"DeviceSpec should be equal after database round-trip")

			// Test: Create equivalent spec with different map ordering
			equivalentSpec := api.DeviceSpec{
				Os: &api.DeviceOsSpec{
					Image: "quay.io/test/os:v1.0.0",
				},
				Config: &[]api.ConfigProviderSpec{gitConfig, inlineConfig},
				Applications: &[]api.ApplicationProviderSpec{
					{
						Name:    lo.ToPtr("test-app"),
						AppType: lo.ToPtr(api.AppTypeCompose),
						EnvVars: &map[string]string{
							"ENV3": "value3", // Different key order
							"ENV1": "value1",
							"ENV2": "value2",
						},
					},
				},
				UpdatePolicy: &api.DeviceUpdatePolicySpec{
					DownloadSchedule: &api.UpdateSchedule{
						At:       "0 2 * * *",
						TimeZone: lo.ToPtr("UTC"),
					},
				},
			}

			// Test: Specs should be equal despite different map ordering
			Expect(api.DeviceSpecsAreEqual(originalSpec, equivalentSpec)).To(BeTrue(),
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

			Expect(api.DeviceSpecsAreEqual(originalSpec, differentSpec)).To(BeFalse(),
				"DeviceSpecs with different configs should not be equal")

			// Test: nil vs empty slice differences
			nilSliceSpec := originalSpec
			nilSliceSpec.Applications = nil

			emptySliceSpec := originalSpec
			emptySliceSpec.Applications = &[]api.ApplicationProviderSpec{}

			Expect(api.DeviceSpecsAreEqual(nilSliceSpec, emptySliceSpec)).To(BeFalse(),
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
			Expect(api.FleetSpecsAreEqual(originalFleetSpec, retrieved.Spec)).To(BeTrue(),
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
			Expect(api.FleetSpecsAreEqual(originalFleetSpec, equivalentFleetSpec)).To(BeTrue(),
				"FleetSpecs should be equal despite different map key ordering")

			// Test: Different rollout policies should not be equal
			differentFleetSpec := originalFleetSpec
			differentFleetSpec.RolloutPolicy = &api.RolloutPolicy{
				DisruptionBudget: &api.DisruptionBudget{
					MaxUnavailable: lo.ToPtr(1), // Different value
				},
			}

			Expect(api.FleetSpecsAreEqual(originalFleetSpec, differentFleetSpec)).To(BeFalse(),
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
