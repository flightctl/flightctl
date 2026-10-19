package tasks_test

import (
	"context"
	"path/filepath"
	"runtime"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

// Labels derived by the mappings that ship in
// packaging/flightctl/label-sync/mappings.yaml. The feature namespace carries
// capability facts, the systeminfo namespace carries allow-listed systemInfo
// fields, and the custominfo namespace carries operator-defined custom info.
const (
	capabilityGpuPresentLabel   = "feature.flightctl.io/gpu.present"
	capabilityKvmEnabledLabel   = "feature.flightctl.io/kvm.enabled"
	capabilityOsModeLabel       = "feature.flightctl.io/os.mode"
	capabilityTpmEnabledLabel   = "feature.flightctl.io/tpm.enabled"
	capabilityArchitectureLabel = "systeminfo.flightctl.io/architecture"
	capabilityAgentVersionLabel = "systeminfo.flightctl.io/agentVersion"
	capabilityDistroIDLabel     = "systeminfo.flightctl.io/distroId"
	capabilityBootIDLabel       = "systeminfo.flightctl.io/bootID"
	capabilitySiteCustomLabel   = "custominfo.flightctl.io/site"
)

// packagedMappingsPath resolves the mapping manifest that ships with the
// product so these specs exercise the shipped expressions rather than a copy
// that can silently drift from them.
func packagedMappingsPath() string {
	GinkgoHelper()
	_, sourceFile, _, ok := runtime.Caller(0)
	Expect(ok).To(BeTrue(), "could not locate the test source file")
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../.."))
	return filepath.Join(repoRoot, "packaging/flightctl/label-sync/mappings.yaml")
}

var _ = Describe("Device capability feature labels", func() {
	var (
		ctx          context.Context
		log          *logrus.Logger
		orgID        uuid.UUID
		cfg          *config.Config
		dbName       string
		db           *gorm.DB
		deviceStore  devicestore.Store
		mappingStore labelsyncmappingstore.Store
		deviceSvc    deviceservice.Service
		mappingSvc   labelsyncmappingservice.Service
		ctrl         *gomock.Controller
		kvStoreInst  kvstore.KVStore
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.EventSourceComponentCtxKey, "flightctl-worker")
		ctx = context.WithValue(ctx, consts.EventActorCtxKey, "service:flightctl-worker")
		orgID = store.NullOrgId
		log = flightlog.InitLogs()

		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())

		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		mappingStore = labelsyncmappingstore.NewStore(db, log.WithField("pkg", "labelsyncmapping-store"))
		evStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))

		ctrl = gomock.NewController(GinkgoT())
		producer := queues.NewMockQueueProducer(ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		eventsSvc := events.NewServiceHandler(evStore, worker_client.NewWorkerClient(producer, log), log)

		kvStoreInst, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).NotTo(HaveOccurred())
		deviceSvc = deviceservice.NewDeviceServiceHandler(deviceStore, nil, nil, eventsSvc, kvStoreInst, "", log)

		mappingService, err := labelsyncmappingservice.NewServiceHandler(mappingStore, deviceStore, mustNewEvaluator(), eventsSvc, log)
		Expect(err).NotTo(HaveOccurred())
		mappingSvc = mappingService
	})

	AfterEach(func() {
		if kvStoreInst != nil {
			kvStoreInst.Close()
		}
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
		ctrl.Finish()
	})

	// provisionDefaultMappings seeds the organization the same way the server
	// seeds a newly created organization at runtime.
	provisionDefaultMappings := func() {
		GinkgoHelper()
		provisioner, err := labelsyncmappingservice.NewInitialLabelSyncMappingProvisioner(packagedMappingsPath(), mappingSvc, log)
		Expect(err).NotTo(HaveOccurred())
		provisioner.Provision(ctx, orgID)
	}

	createDevice := func(name string, systemInfo api.DeviceSystemInfo, labels map[string]string) {
		GinkgoHelper()
		deviceStatus := domain.NewDeviceStatus()
		deviceStatus.SystemInfo = systemInfo
		device := domain.Device{
			ApiVersion: domain.DeviceAPIVersion,
			Kind:       domain.DeviceKind,
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr(name),
				Labels: &labels,
			},
			Spec:   &domain.DeviceSpec{},
			Status: &deviceStatus,
		}
		_, status := deviceSvc.CreateDevice(ctx, orgID, device)
		Expect(status.Code).To(Equal(int32(201)), status.Message)
	}

	reportSystemInfo := func(name string, systemInfo api.DeviceSystemInfo) {
		GinkgoHelper()
		info, err := util.StructToMap(systemInfo)
		Expect(err).NotTo(HaveOccurred())
		var value interface{} = info
		_, status := deviceSvc.PatchDeviceStatus(ctx, orgID, name, domain.PatchRequest{
			{Op: "replace", Path: "/status/systemInfo", Value: &value},
		}, false)
		Expect(status.Code).To(Equal(int32(200)), status.Message)
	}

	// reconcile runs the same worker logic the label-sync worker runs when a
	// device reports a new status.
	reconcile := func(name string) {
		GinkgoHelper()
		logic, err := tasks.NewDeviceLabelReconciliationLogic(log, mappingSvc, orgID, domain.Event{
			Reason: domain.EventReasonResourceUpdated,
			InvolvedObject: domain.ObjectReference{
				Kind: domain.DeviceKind,
				Name: name,
			},
		})
		Expect(err).NotTo(HaveOccurred())
		Expect(logic.Reconcile(ctx)).To(Succeed())
	}

	deviceLabels := func(name string) map[string]string {
		GinkgoHelper()
		persisted, err := deviceStore.Get(ctx, orgID, name)
		Expect(err).NotTo(HaveOccurred())
		return lo.FromPtr(persisted.Metadata.Labels)
	}

	// listDeviceNames exercises the derived labels through the same label
	// selector path an operator uses to filter the device list.
	listDeviceNames := func(labelSelector string) []string {
		GinkgoHelper()
		list, status := deviceSvc.ListDevices(ctx, orgID, domain.ListDevicesParams{
			LabelSelector: lo.ToPtr(labelSelector),
		}, nil)
		Expect(status.Code).To(Equal(int32(200)), status.Message)
		names := make([]string, 0, len(list.Items))
		for _, item := range list.Items {
			names = append(names, lo.FromPtr(item.Metadata.Name))
		}
		return names
	}

	// reportingSystemInfo is what any supported agent reports. Capability
	// fields are left unset so each spec can opt in to the ones it needs.
	reportingSystemInfo := func() api.DeviceSystemInfo {
		return api.DeviceSystemInfo{
			AgentVersion:    "1.4.0",
			Architecture:    "amd64",
			BootID:          "0f1e2d3c-4b5a-6978-8796-a5b4c3d2e1f0",
			OperatingSystem: "linux",
			AdditionalProperties: map[string]string{
				"distroId":      "rhel",
				"distroVersion": "9.4",
			},
		}
	}

	Context("Provisioning the packaged mappings", func() {
		It("When an organization is provisioned it should install every packaged mapping with its declared key", func() {
			provisionDefaultMappings()

			list, status := mappingSvc.ListLabelSyncMappings(ctx, orgID, domain.ListLabelSyncMappingsParams{})
			Expect(status.Code).To(Equal(int32(200)), status.Message)

			keysByName := make(map[string]*string, len(list.Items))
			for _, mapping := range list.Items {
				Expect(mapping.Spec.ResourceType).To(Equal(domain.LabelSyncMappingDevice))
				keysByName[lo.FromPtr(mapping.Metadata.Name)] = mapping.Spec.Key
			}

			Expect(keysByName).To(HaveKeyWithValue("gpu-present", lo.ToPtr(capabilityGpuPresentLabel)))
			Expect(keysByName).To(HaveKeyWithValue("kvm-enabled", lo.ToPtr(capabilityKvmEnabledLabel)))
			Expect(keysByName).To(HaveKeyWithValue("os-mode", lo.ToPtr(capabilityOsModeLabel)))
			Expect(keysByName).To(HaveKeyWithValue("tpm-enabled", lo.ToPtr(capabilityTpmEnabledLabel)))
			Expect(keysByName).To(HaveKey("system-info"))
			Expect(keysByName).To(HaveKey("custom-info"))
			Expect(keysByName["system-info"]).To(BeNil(), "the systemInfo mapping must emit a label map")
			Expect(keysByName["custom-info"]).To(BeNil(), "the customInfo mapping must emit a label map")
		})

		It("When provisioning runs twice it should keep a single copy of each mapping", func() {
			provisionDefaultMappings()
			first, status := mappingSvc.ListLabelSyncMappings(ctx, orgID, domain.ListLabelSyncMappingsParams{})
			Expect(status.Code).To(Equal(int32(200)), status.Message)
			Expect(first.Items).ToNot(BeEmpty())

			provisionDefaultMappings()
			second, status := mappingSvc.ListLabelSyncMappings(ctx, orgID, domain.ListLabelSyncMappingsParams{})
			Expect(status.Code).To(Equal(int32(200)), status.Message)
			Expect(second.Items).To(HaveLen(len(first.Items)))
		})
	})

	Context("Deriving feature labels from reported capabilities", func() {
		BeforeEach(provisionDefaultMappings)

		It("When a device reports a GPU, KVM, TPM, and image OS mode it should derive matching feature labels", func() {
			systemInfo := reportingSystemInfo()
			systemInfo.Gpus = &[]api.DeviceGpu{{Index: 0, Vendor: lo.ToPtr("NVIDIA"), Model: lo.ToPtr("RTX 4090")}}
			systemInfo.Kvm = &api.DeviceKvm{Enabled: lo.ToPtr(true)}
			systemInfo.OsMode = lo.ToPtr(domain.OsModeImage)
			systemInfo.AdditionalProperties["tpmVendorInfo"] = "IFX"
			createDevice("capable-device", systemInfo, map[string]string{"site": "east"})

			reconcile("capable-device")

			labels := deviceLabels("capable-device")
			Expect(labels).To(HaveKeyWithValue(capabilityGpuPresentLabel, "true"))
			Expect(labels).To(HaveKeyWithValue(capabilityKvmEnabledLabel, "true"))
			Expect(labels).To(HaveKeyWithValue(capabilityOsModeLabel, string(domain.OsModeImage)))
			Expect(labels).To(HaveKeyWithValue(capabilityTpmEnabledLabel, "true"))
			Expect(labels).To(HaveKeyWithValue(capabilityArchitectureLabel, "amd64"))
			Expect(labels).To(HaveKeyWithValue(capabilityAgentVersionLabel, "1.4.0"))
			Expect(labels).To(HaveKeyWithValue(capabilityDistroIDLabel, "rhel"))
			Expect(labels).To(HaveKeyWithValue("site", "east"), "operator labels must survive derivation")
		})

		It("When a device reports no GPU and disabled KVM it should derive explicit false feature labels", func() {
			systemInfo := reportingSystemInfo()
			systemInfo.Gpus = &[]api.DeviceGpu{}
			systemInfo.Kvm = &api.DeviceKvm{Enabled: lo.ToPtr(false)}
			systemInfo.OsMode = lo.ToPtr(domain.OsModePackage)
			createDevice("plain-device", systemInfo, map[string]string{})

			reconcile("plain-device")

			labels := deviceLabels("plain-device")
			Expect(labels).To(HaveKeyWithValue(capabilityGpuPresentLabel, "false"))
			Expect(labels).To(HaveKeyWithValue(capabilityKvmEnabledLabel, "false"))
			Expect(labels).To(HaveKeyWithValue(capabilityOsModeLabel, string(domain.OsModePackage)))
		})

		It("When a device never reports a capability it should omit the feature label instead of deriving false", func() {
			createDevice("legacy-device", reportingSystemInfo(), map[string]string{})

			reconcile("legacy-device")

			labels := deviceLabels("legacy-device")
			Expect(labels).ToNot(HaveKey(capabilityGpuPresentLabel))
			Expect(labels).ToNot(HaveKey(capabilityKvmEnabledLabel))
			Expect(labels).ToNot(HaveKey(capabilityOsModeLabel))
			Expect(labels).ToNot(HaveKey(capabilityTpmEnabledLabel))
			Expect(labels).To(HaveKeyWithValue(capabilityArchitectureLabel, "amd64"),
				"fields a legacy agent does report must still derive labels")
		})

		It("When KVM is reported without the enabled field it should omit the KVM feature label", func() {
			systemInfo := reportingSystemInfo()
			systemInfo.Kvm = &api.DeviceKvm{}
			createDevice("partial-kvm-device", systemInfo, map[string]string{})

			reconcile("partial-kvm-device")

			Expect(deviceLabels("partial-kvm-device")).ToNot(HaveKey(capabilityKvmEnabledLabel))
		})

		It("When a device reports customInfo it should derive namespaced customInfo labels", func() {
			systemInfo := reportingSystemInfo()
			systemInfo.CustomInfo = &api.CustomDeviceInfo{"site": "factory-floor-2"}
			createDevice("custom-info-device", systemInfo, map[string]string{})

			reconcile("custom-info-device")

			labels := deviceLabels("custom-info-device")
			Expect(labels).To(HaveKeyWithValue(capabilitySiteCustomLabel, "factory-floor-2"))
			Expect(labels).ToNot(HaveKey(capabilityBootIDLabel),
				"only allow-listed systemInfo fields become labels")
		})

		It("When reported capabilities change it should update the derived feature labels", func() {
			systemInfo := reportingSystemInfo()
			systemInfo.Gpus = &[]api.DeviceGpu{{Index: 0, Vendor: lo.ToPtr("NVIDIA")}}
			systemInfo.Kvm = &api.DeviceKvm{Enabled: lo.ToPtr(true)}
			createDevice("changing-device", systemInfo, map[string]string{})
			reconcile("changing-device")
			Expect(deviceLabels("changing-device")).To(HaveKeyWithValue(capabilityGpuPresentLabel, "true"))

			updated := reportingSystemInfo()
			updated.Gpus = &[]api.DeviceGpu{}
			updated.Kvm = &api.DeviceKvm{Enabled: lo.ToPtr(false)}
			reportSystemInfo("changing-device", updated)

			reconcile("changing-device")

			labels := deviceLabels("changing-device")
			Expect(labels).To(HaveKeyWithValue(capabilityGpuPresentLabel, "false"))
			Expect(labels).To(HaveKeyWithValue(capabilityKvmEnabledLabel, "false"))
		})

		It("When a capability stops being reported it should remove the derived feature label", func() {
			systemInfo := reportingSystemInfo()
			systemInfo.Gpus = &[]api.DeviceGpu{{Index: 0}}
			createDevice("silenced-device", systemInfo, map[string]string{})
			reconcile("silenced-device")
			Expect(deviceLabels("silenced-device")).To(HaveKeyWithValue(capabilityGpuPresentLabel, "true"))

			reportSystemInfo("silenced-device", reportingSystemInfo())

			reconcile("silenced-device")

			Expect(deviceLabels("silenced-device")).ToNot(HaveKey(capabilityGpuPresentLabel))
		})

		It("When an operator sets a reserved feature label it should reconcile it to the derived value", func() {
			systemInfo := reportingSystemInfo()
			systemInfo.Gpus = &[]api.DeviceGpu{}
			createDevice("spoofed-device", systemInfo, map[string]string{
				"site":                    "main",
				capabilityGpuPresentLabel: "true",
			})

			reconcile("spoofed-device")

			labels := deviceLabels("spoofed-device")
			Expect(labels).To(HaveKeyWithValue(capabilityGpuPresentLabel, "false"), "the reserved namespace is server owned")
			Expect(labels).To(HaveKeyWithValue("site", "main"), "operator labels outside the namespace are untouched")
		})

		It("When an operator edits an already derived feature label it should reject the change", func() {
			systemInfo := reportingSystemInfo()
			systemInfo.Gpus = &[]api.DeviceGpu{}
			createDevice("protected-device", systemInfo, map[string]string{"site": "main"})
			reconcile("protected-device")
			Expect(deviceLabels("protected-device")).To(HaveKeyWithValue(capabilityGpuPresentLabel, "false"))

			var spoofed interface{} = map[string]string{"site": "main", capabilityGpuPresentLabel: "true"}
			_, status := deviceSvc.PatchDevice(ctx, orgID, "protected-device", domain.PatchRequest{
				{Op: "replace", Path: "/metadata/labels", Value: &spoofed},
			}, true, true)
			Expect(status.Code).To(Equal(int32(409)), status.Message)

			persisted, err := deviceStore.Get(ctx, orgID, "protected-device")
			Expect(err).NotTo(HaveOccurred())
			replacement := *persisted
			replacementLabels := map[string]string{"site": "main", capabilityGpuPresentLabel: "true"}
			replacement.Metadata.Labels = &replacementLabels
			_, status = deviceSvc.ReplaceDevice(ctx, orgID, "protected-device", replacement, nil, true, true)
			Expect(status.Code).To(Equal(int32(409)), status.Message)

			labels := deviceLabels("protected-device")
			Expect(labels).To(HaveKeyWithValue(capabilityGpuPresentLabel, "false"))
			Expect(labels).To(HaveKeyWithValue("site", "main"))
		})

		It("When an operator edits labels outside the reserved namespace it should keep the derived labels", func() {
			systemInfo := reportingSystemInfo()
			systemInfo.Gpus = &[]api.DeviceGpu{{Index: 0}}
			createDevice("operator-edit-device", systemInfo, map[string]string{"site": "main"})
			reconcile("operator-edit-device")

			derived := deviceLabels("operator-edit-device")
			operatorLabels := map[string]string{"site": "west", "tier": "edge"}
			for key, value := range derived {
				if key == "site" {
					continue
				}
				operatorLabels[key] = value
			}
			var replacement interface{} = operatorLabels
			_, status := deviceSvc.PatchDevice(ctx, orgID, "operator-edit-device", domain.PatchRequest{
				{Op: "replace", Path: "/metadata/labels", Value: &replacement},
			}, true, true)
			Expect(status.Code).To(Equal(int32(200)), status.Message)

			labels := deviceLabels("operator-edit-device")
			Expect(labels).To(HaveKeyWithValue("site", "west"))
			Expect(labels).To(HaveKeyWithValue("tier", "edge"))
			Expect(labels).To(HaveKeyWithValue(capabilityGpuPresentLabel, "true"), "derived labels survive operator label edits")
		})

		It("When devices differ by capability it should return only the matching devices for a feature label selector", func() {
			gpuSystemInfo := reportingSystemInfo()
			gpuSystemInfo.Gpus = &[]api.DeviceGpu{{Index: 0}}
			gpuSystemInfo.OsMode = lo.ToPtr(domain.OsModeImage)
			createDevice("gpu-device", gpuSystemInfo, map[string]string{})

			noGPUSystemInfo := reportingSystemInfo()
			noGPUSystemInfo.Gpus = &[]api.DeviceGpu{}
			noGPUSystemInfo.OsMode = lo.ToPtr(domain.OsModePackage)
			createDevice("no-gpu-device", noGPUSystemInfo, map[string]string{})

			createDevice("unreported-device", reportingSystemInfo(), map[string]string{})

			for _, name := range []string{"gpu-device", "no-gpu-device", "unreported-device"} {
				reconcile(name)
			}

			Expect(listDeviceNames(capabilityGpuPresentLabel + "=true")).To(ConsistOf("gpu-device"))
			Expect(listDeviceNames(capabilityGpuPresentLabel + "=false")).To(ConsistOf("no-gpu-device"))
			Expect(listDeviceNames(capabilityGpuPresentLabel + " in (true,false)")).To(ConsistOf("gpu-device", "no-gpu-device"))
			Expect(listDeviceNames(capabilityGpuPresentLabel + "!=true")).To(ConsistOf("no-gpu-device", "unreported-device"))
			Expect(listDeviceNames("!" + capabilityGpuPresentLabel)).To(ConsistOf("unreported-device"))
			Expect(listDeviceNames(capabilityOsModeLabel + "=image")).To(ConsistOf("gpu-device"))
		})
	})

	Context("Mapping lifecycle and failure isolation", func() {
		It("When no mappings exist it should leave the device labels untouched", func() {
			systemInfo := reportingSystemInfo()
			systemInfo.Gpus = &[]api.DeviceGpu{{Index: 0}}
			createDevice("unmapped-device", systemInfo, map[string]string{"site": "east"})

			reconcile("unmapped-device")

			labels := deviceLabels("unmapped-device")
			Expect(labels).To(HaveKeyWithValue("site", "east"))
			Expect(labels).ToNot(HaveKey(capabilityGpuPresentLabel))
		})

		It("When a capability mapping is deleted it should remove the labels it owned", func() {
			provisionDefaultMappings()
			systemInfo := reportingSystemInfo()
			systemInfo.Gpus = &[]api.DeviceGpu{{Index: 0}}
			systemInfo.Kvm = &api.DeviceKvm{Enabled: lo.ToPtr(true)}
			createDevice("retired-mapping-device", systemInfo, map[string]string{"site": "east"})
			reconcile("retired-mapping-device")
			Expect(deviceLabels("retired-mapping-device")).To(HaveKeyWithValue(capabilityGpuPresentLabel, "true"))

			Expect(mappingSvc.DeleteLabelSyncMapping(ctx, orgID, "gpu-present").Code).To(Equal(int32(200)))

			reconcile("retired-mapping-device")

			labels := deviceLabels("retired-mapping-device")
			Expect(labels).ToNot(HaveKey(capabilityGpuPresentLabel))
			Expect(labels).To(HaveKeyWithValue(capabilityKvmEnabledLabel, "true"), "unrelated mappings keep deriving")
			Expect(labels).To(HaveKeyWithValue("site", "east"))
		})

		It("When one mapping fails at evaluation time it should still derive the other capability labels", func() {
			provisionDefaultMappings()
			// Compiles, but fails at runtime because the architecture is not an integer.
			broken := domain.LabelSyncMapping{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr("broken-capability")},
				Spec: domain.LabelSyncMappingSpec{
					ResourceType: domain.LabelSyncMappingDevice,
					Key:          lo.ToPtr("feature.flightctl.io/broken"),
					Expression:   "string(int(status.systemInfo.architecture))",
				},
			}
			_, status := mappingSvc.CreateLabelSyncMapping(ctx, orgID, broken)
			Expect(status.Code).To(Equal(int32(201)), status.Message)

			systemInfo := reportingSystemInfo()
			systemInfo.Gpus = &[]api.DeviceGpu{{Index: 0}}
			systemInfo.Kvm = &api.DeviceKvm{Enabled: lo.ToPtr(true)}
			systemInfo.OsMode = lo.ToPtr(domain.OsModeImage)
			createDevice("isolated-failure-device", systemInfo, map[string]string{})

			reconcile("isolated-failure-device")

			labels := deviceLabels("isolated-failure-device")
			Expect(labels).ToNot(HaveKey("feature.flightctl.io/broken"))
			Expect(labels).To(HaveKeyWithValue(capabilityGpuPresentLabel, "true"))
			Expect(labels).To(HaveKeyWithValue(capabilityKvmEnabledLabel, "true"))
			Expect(labels).To(HaveKeyWithValue(capabilityOsModeLabel, string(domain.OsModeImage)))
		})
	})
})
