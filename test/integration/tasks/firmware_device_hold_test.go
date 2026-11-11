package tasks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	templateversionstore "github.com/flightctl/flightctl/internal/store/templateversion"
	"github.com/flightctl/flightctl/internal/tasks"
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

var outstandingFirmwarePhases = []api.FirmwareAttemptPhase{
	api.FirmwareAttemptPhasePreparing,
	api.FirmwareAttemptPhaseWaitingForPolicy,
	api.FirmwareAttemptPhaseStaging,
	api.FirmwareAttemptPhaseAwaitingReboot,
	api.FirmwareAttemptPhaseVerifying,
	"Mystery",
	"",
}

var terminalFirmwarePhases = []api.FirmwareAttemptPhase{
	api.FirmwareAttemptPhaseSucceeded,
	api.FirmwareAttemptPhaseFailed,
	api.FirmwareAttemptPhaseCanceled,
}

var _ = Describe("Device firmware hold", func() {
	const fleetName = "hold-fleet"
	var (
		log              *logrus.Logger
		ctx              context.Context
		orgId            uuid.UUID
		cfg              *config.Config
		dbName           string
		db               *gorm.DB
		deviceStore      devicestore.Store
		concurrentStore  devicestore.Store
		fleetStore       fleetstore.Store
		tvStore          templateversionstore.Store
		kvStore          kvstore.KVStore
		deviceSvc        deviceservice.Service
		repositorySvc    repositoryservice.Service
		fleetSvc         fleetservice.Service
		tvSvc            templateversionservice.Service
		dependencyrefSvc dependencyrefservice.Service
		queuesProvider   queues.Provider
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		concurrentStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "concurrent-device-store"))
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		tvStore = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		producer := queues.NewMockQueueProducer(gomock.NewController(GinkgoT()))
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		workerClient := worker_client.NewWorkerClient(producer, log)
		kvStore, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())
		eventsSvc := events.NewServiceHandler(eventstore.NewEventStore(db, log.WithField("pkg", "event-store")), workerClient, log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(deviceStore, nil, fleetStore, eventsSvc, kvStore, "", log)
		repositorySvc = repositoryservice.NewServiceHandler(repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store")), eventsSvc, log)
		fleetSvc = fleetservice.NewServiceHandler(fleetStore, nil, eventsSvc, log)
		tvSvc = templateversionservice.NewServiceHandler(tvStore, kvStore, eventsSvc, log)
		dependencyrefSvc = dependencyrefservice.NewServiceHandler(dependencyrefstore.NewDependencyRefStore(db, log.WithField("pkg", "dependencyref-store")), log)
		if queuesProvider == nil {
			queuesProvider, err = queues.NewRedisProvider(ctx, log, fmt.Sprintf("device-hold-test-%s", uuid.NewString()), redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
			Expect(err).ToNot(HaveOccurred())
			Expect(rendered.Bus.Initialize(ctx, kvStore, queuesProvider, 10*time.Second, log)).To(Succeed())
		}
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	uniqueName := func(prefix string) string { return prefix + "-" + uuid.NewString()[:8] }
	inlineConfig := func(content string) *[]api.ConfigProviderSpec {
		item := api.ConfigProviderSpec{}
		Expect(item.FromInlineConfigProviderSpec(api.InlineConfigProviderSpec{
			Name:   "app-config",
			Inline: []api.FileSpec{{Path: "/etc/app.conf", Content: content}},
		})).To(Succeed())
		return &[]api.ConfigProviderSpec{item}
	}
	createDevice := func(name string, owner *string, spec api.DeviceSpec) {
		status := api.NewDeviceStatus()
		_, err := deviceStore.Create(ctx, orgId, &api.Device{Metadata: api.ObjectMeta{Name: lo.ToPtr(name), Owner: owner}, Spec: &spec, Status: &status}, nil)
		Expect(err).ToNot(HaveOccurred())
	}
	getDevice := func(name string) *api.Device {
		device, err := deviceStore.Get(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		return device
	}
	setFirmwareStatus := func(s devicestore.Store, name string, firmware *api.FirmwareStatus) {
		device := getDevice(name)
		device.Status.Firmware = firmware
		_, _, err := s.UpdateStatus(ctx, orgId, device, nil)
		Expect(err).ToNot(HaveOccurred())
	}
	changeSpec := func(s devicestore.Store, name string, change func(*api.DeviceSpec)) {
		_, _, _, err := s.Mutate(ctx, orgId, name, nil, func(m *devicestore.DeviceMutation) error {
			if err := m.RequireExisting(); err != nil {
				return err
			}
			spec := lo.FromPtr(m.Device.Spec)
			change(&spec)
			m.Device.Spec = &spec
			return nil
		})
		Expect(err).ToNot(HaveOccurred())
	}
	firmwareJSON := func(device *api.Device) string {
		if device.Status == nil || device.Status.Firmware == nil {
			return ""
		}
		out, err := json.Marshal(device.Status.Firmware)
		Expect(err).ToNot(HaveOccurred())
		return string(out)
	}
	// decodedJSON returns v as decoded JSON, so values compare without regard
	// to the key order of stored JSON.
	decodedJSON := func(v any) any {
		out, err := json.Marshal(v)
		Expect(err).ToNot(HaveOccurred())
		var decoded any
		Expect(json.Unmarshal(out, &decoded)).To(Succeed())
		return decoded
	}
	specJSON := func(device *api.Device) string {
		out, err := json.Marshal(device.Spec)
		Expect(err).ToNot(HaveOccurred())
		return string(out)
	}
	deviceEventReasons := func(name string) []string {
		var reasons []string
		Expect(db.WithContext(ctx).Raw("SELECT reason FROM events WHERE org_id = ? AND involved_object_kind = ? AND involved_object_name = ? ORDER BY created_at, name",
			orgId, api.DeviceKind, name).Scan(&reasons).Error).To(Succeed())
		return reasons
	}

	// renderedState is everything a rendered write would change on a device.
	type renderedState struct {
		Annotations          map[string]string
		RenderedConfig       *string
		RenderedApplications *string
		RenderedOs           *string
		RenderTimestamp      time.Time
		DependencySync       string
		NotifiedVersion      string
	}
	renderedStateOf := func(name string) renderedState {
		var row struct {
			RenderedConfig       *string
			RenderedApplications *string
			RenderedOs           *string
			RenderTimestamp      time.Time
		}
		Expect(db.WithContext(ctx).Raw(`SELECT rendered_config::text AS rendered_config, rendered_applications::text AS rendered_applications,
			rendered_os::text AS rendered_os, render_timestamp FROM devices WHERE org_id = ? AND name = ?`, orgId, name).Scan(&row).Error).To(Succeed())
		device := getDevice(name)
		dependencySync, err := json.Marshal(device.Status.DependencySync)
		Expect(err).ToNot(HaveOccurred())
		notified, err := kvStore.Get(ctx, fmt.Sprintf("v1/%s/device/%s/rendered", orgId, name))
		Expect(err).ToNot(HaveOccurred())
		return renderedState{
			Annotations: lo.PickByKeys(lo.FromPtr(device.Metadata.Annotations), []string{
				api.DeviceAnnotationRenderedVersion, api.DeviceAnnotationRenderedTemplateVersion, api.DeviceAnnotationRenderedSpecHash,
			}),
			RenderedConfig:       row.RenderedConfig,
			RenderedApplications: row.RenderedApplications,
			RenderedOs:           row.RenderedOs,
			RenderTimestamp:      row.RenderTimestamp,
			DependencySync:       string(dependencySync),
			NotifiedVersion:      string(notified),
		}
	}
	renderDevice := func(name string) error {
		event := api.Event{Reason: api.EventReasonResourceUpdated, InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: name}}
		return tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, nil, nil, kvStore, nil).RenderDevice(ctx, orgId, event)
	}
	expectHoldCondition := func(device *api.Device) {
		condition := conditionOf(device.Status.Conditions, api.ConditionTypeDeviceSpecValid)
		Expect(condition).ToNot(BeNil())
		Expect(condition.Status).To(Equal(api.ConditionStatusUnknown))
		Expect(condition.Reason).To(Equal(api.DeviceSpecValidReasonFirmwareUnsupported))
	}
	expectSpecValid := func(device *api.Device) {
		condition := conditionOf(device.Status.Conditions, api.ConditionTypeDeviceSpecValid)
		Expect(condition).ToNot(BeNil())
		Expect(condition.Status).To(Equal(api.ConditionStatusTrue))
	}
	// renderedDevice creates a standalone device without firmware and renders
	// it once, so later renders can be compared with a rendered baseline.
	renderedDevice := func(prefix string) string {
		name := uniqueName(prefix)
		createDevice(name, nil, api.DeviceSpec{Config: inlineConfig("v1")})
		Expect(renderDevice(name)).To(Succeed())
		Expect(renderedStateOf(name).Annotations).To(HaveKeyWithValue(api.DeviceAnnotationRenderedVersion, "1"))
		return name
	}
	// expectHeldRender renders the device and checks that nothing a rendered
	// write changes moved, that its spec and reported firmware are kept, and
	// that the hold condition is recorded; a second render writes nothing.
	expectHeldRender := func(name string) {
		before := getDevice(name)
		renderedBefore := renderedStateOf(name)
		eventsBefore := deviceEventReasons(name)

		Expect(renderDevice(name)).To(Succeed())

		after := getDevice(name)
		Expect(renderedStateOf(name)).To(Equal(renderedBefore))
		Expect(specJSON(after)).To(Equal(specJSON(before)))
		Expect(firmwareJSON(after)).To(Equal(firmwareJSON(before)))
		Expect(after.Metadata.Generation).To(Equal(before.Metadata.Generation))
		expectHoldCondition(after)
		Expect(deviceEventReasons(name)[len(eventsBefore):]).ToNot(ContainElement(string(api.EventReasonResourceUpdated)))

		By("rendering the held device again, nothing is written")
		eventsAfterHold := deviceEventReasons(name)
		Expect(renderDevice(name)).To(Succeed())
		again := getDevice(name)
		Expect(again.Metadata.ResourceVersion).To(Equal(after.Metadata.ResourceVersion))
		Expect(again.Status.Conditions).To(Equal(after.Status.Conditions))
		Expect(renderedStateOf(name)).To(Equal(renderedBefore))
		Expect(deviceEventReasons(name)).To(Equal(eventsAfterHold))
	}
	expectNormalRender := func(name string) {
		before := getDevice(name)
		Expect(renderDevice(name)).To(Succeed())
		after := getDevice(name)
		Expect(renderedStateOf(name).Annotations).To(HaveKeyWithValue(api.DeviceAnnotationRenderedVersion, "2"))
		Expect(firmwareJSON(after)).To(Equal(firmwareJSON(before)))
		expectSpecValid(after)
	}

	Context("rendered writes", func() {
		It("When a standalone device has desired firmware it should hold its rendered output and keep its firmware", func() {
			name := renderedDevice("desired")
			changeSpec(deviceStore, name, func(spec *api.DeviceSpec) {
				spec.Firmware = holdFirmwareSpec("firmware-repo")
				spec.Config = inlineConfig("v2")
			})
			expectHeldRender(name)
			Expect(getDevice(name).Spec.Firmware).To(Equal(holdFirmwareSpec("firmware-repo")))
		})

		for _, phase := range outstandingFirmwarePhases {
			It(fmt.Sprintf("When a device without desired firmware has an outstanding %q attempt it should hold its rendered output and keep the attempt", phase), func() {
				name := renderedDevice("outstanding")
				setFirmwareStatus(deviceStore, name, holdFirmwareStatus(phase))
				changeSpec(deviceStore, name, func(spec *api.DeviceSpec) { spec.Config = inlineConfig("v2") })
				Expect(getDevice(name).Spec.Firmware).To(BeNil())
				expectHeldRender(name)
			})
		}

		for _, phase := range terminalFirmwarePhases {
			It(fmt.Sprintf("When a device without desired firmware has only a %s attempt it should render normally and keep the attempt", phase), func() {
				name := renderedDevice("terminal")
				setFirmwareStatus(deviceStore, name, holdFirmwareStatus(phase))
				changeSpec(deviceStore, name, func(spec *api.DeviceSpec) { spec.Config = inlineConfig("v2") })
				expectNormalRender(name)
			})
		}

		It("When a device has no firmware state it should render normally", func() {
			name := renderedDevice("unmanaged")
			changeSpec(deviceStore, name, func(spec *api.DeviceSpec) { spec.Config = inlineConfig("v2") })
			expectNormalRender(name)
		})

		It("When desired firmware is removed while an attempt is outstanding it should hold rendering until the attempt ends", func() {
			name := renderedDevice("withdrawn")
			changeSpec(deviceStore, name, func(spec *api.DeviceSpec) {
				spec.Firmware = holdFirmwareSpec("firmware-repo")
				spec.Config = inlineConfig("v2")
			})
			setFirmwareStatus(deviceStore, name, holdFirmwareStatus(api.FirmwareAttemptPhaseStaging))
			expectHeldRender(name)

			_, status := deviceSvc.PatchDevice(ctx, orgId, name, api.PatchRequest{{Op: "remove", Path: "/spec/firmware"}}, false, false)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
			Expect(getDevice(name).Spec.Firmware).To(BeNil())
			expectHeldRender(name)

			setFirmwareStatus(deviceStore, name, holdFirmwareStatus(api.FirmwareAttemptPhaseSucceeded))
			changeSpec(deviceStore, name, func(spec *api.DeviceSpec) { spec.Config = inlineConfig("v3") })
			expectNormalRender(name)
		})
	})

	Context("renders of an unchanged spec", func() {
		renderFor := func(name string, reason api.EventReason) error {
			event := api.Event{Reason: reason, InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: name}}
			return tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, nil, nil, kvStore, nil).RenderDevice(ctx, orgId, event)
		}
		// reportStatus writes the agent status of the device through the device
		// service, reporting the rendered version it runs and firmware.
		reportStatus := func(name string, firmware *api.FirmwareStatus) {
			status := *getDevice(name).Status
			status.Config.RenderedVersion = "1"
			status.Firmware = firmware
			status.Conditions = lo.Filter(status.Conditions, func(c api.Condition, _ int) bool { return !c.Type.IsServiceConditionType() })
			_, result := deviceSvc.ReplaceDeviceStatus(ctx, orgId, name, api.Device{Metadata: api.ObjectMeta{Name: lo.ToPtr(name)}, Status: &status}, false)
			Expect(result.Code).To(BeEquivalentTo(http.StatusOK), result.Message)
		}
		expectUpdated := func(name string, want api.DeviceUpdatedStatusType) {
			Expect(getDevice(name).Status.Updated.Status).To(Equal(want))
		}

		It("When an outstanding attempt is reported on a rendered device and a render arrives for the unchanged spec it should record the hold", func() {
			name := renderedDevice("same-hash-attempt")
			reportStatus(name, nil)
			expectUpdated(name, api.DeviceUpdatedStatusUpToDate)
			reportStatus(name, holdFirmwareStatus(api.FirmwareAttemptPhaseStaging))
			renderedBefore := renderedStateOf(name)

			Expect(renderFor(name, api.EventReasonReferencedRepositoryUpdated)).To(Succeed())

			after := getDevice(name)
			expectHoldCondition(after)
			expectUpdated(name, api.DeviceUpdatedStatusOutOfDate)
			Expect(renderedStateOf(name)).To(Equal(renderedBefore))
			Expect(after.Status.Firmware).To(Equal(holdFirmwareStatus(api.FirmwareAttemptPhaseStaging)))

			By("rendering the unchanged spec again, nothing is written")
			Expect(renderFor(name, api.EventReasonReferencedRepositoryUpdated)).To(Succeed())
			Expect(getDevice(name).Metadata.ResourceVersion).To(Equal(after.Metadata.ResourceVersion))
		})

		It("When firmware is removed so the spec matches the last render and that render arrives it should mark the spec valid again", func() {
			name := renderedDevice("same-hash-withdrawn")
			reportStatus(name, nil)
			changeSpec(deviceStore, name, func(spec *api.DeviceSpec) { spec.Firmware = holdFirmwareSpec("firmware-repo") })
			Expect(renderDevice(name)).To(Succeed())
			expectHoldCondition(getDevice(name))
			renderedBefore := renderedStateOf(name)

			_, status := deviceSvc.PatchDevice(ctx, orgId, name, api.PatchRequest{{Op: "remove", Path: "/spec/firmware"}}, false, false)
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
			Expect(renderDevice(name)).To(Succeed())

			after := getDevice(name)
			expectSpecValid(after)
			expectUpdated(name, api.DeviceUpdatedStatusUpToDate)
			Expect(renderedStateOf(name)).To(Equal(renderedBefore), "an unchanged spec renders nothing new")
			Expect(after.Spec.Firmware).To(BeNil())
		})

		It("When an outstanding attempt ends and a render arrives for the unchanged spec it should mark the spec valid again", func() {
			name := renderedDevice("same-hash-ended")
			reportStatus(name, nil)
			reportStatus(name, holdFirmwareStatus(api.FirmwareAttemptPhaseVerifying))
			Expect(renderFor(name, api.EventReasonDependencyChangeDetected)).To(Succeed())
			expectHoldCondition(getDevice(name))
			renderedBefore := renderedStateOf(name)

			reportStatus(name, holdFirmwareStatus(api.FirmwareAttemptPhaseSucceeded))
			Expect(renderFor(name, api.EventReasonReferencedRepositoryUpdated)).To(Succeed())

			after := getDevice(name)
			expectSpecValid(after)
			expectUpdated(name, api.DeviceUpdatedStatusUpToDate)
			Expect(renderedStateOf(name)).To(Equal(renderedBefore))
			Expect(after.Status.Firmware).To(Equal(holdFirmwareStatus(api.FirmwareAttemptPhaseSucceeded)))
		})

		It("When a device without firmware state gets a render for the unchanged spec it should skip it as before", func() {
			name := renderedDevice("same-hash-plain")
			before := getDevice(name)

			Expect(renderFor(name, api.EventReasonReferencedRepositoryUpdated)).To(Succeed())

			Expect(getDevice(name).Metadata.ResourceVersion).To(Equal(before.Metadata.ResourceVersion))
		})
	})

	Context("rendered write races", func() {
		updateRendered := func(name, specHash string) api.Status {
			return deviceSvc.UpdateRenderedDevice(ctx, orgId, name, "[]", "[]", specHash, "", nil, false, nil)
		}

		It("When an outstanding attempt is committed between the hold check and the rendered write it should retry and hold", func() {
			name := renderedDevice("race-attempt")
			before := renderedStateOf(name)
			calls := 0
			deviceStore.SetIntegrationTestCreateOrUpdateCallback(func() {
				calls++
				if calls == 1 {
					setFirmwareStatus(concurrentStore, name, holdFirmwareStatus(api.FirmwareAttemptPhaseStaging))
				}
			})

			status := updateRendered(name, "new-spec-hash")

			Expect(deviceservice.IsFirmwareHold(status)).To(BeTrue(), status.Message)
			Expect(calls).To(BeNumerically(">=", 2))
			after := getDevice(name)
			Expect(renderedStateOf(name)).To(Equal(before))
			Expect(after.Status.Firmware).To(Equal(holdFirmwareStatus(api.FirmwareAttemptPhaseStaging)))
			expectHoldCondition(after)
		})

		It("When desired firmware is committed between the hold check and the rendered write it should retry and hold", func() {
			name := renderedDevice("race-spec")
			before := renderedStateOf(name)
			calls := 0
			deviceStore.SetIntegrationTestCreateOrUpdateCallback(func() {
				calls++
				if calls == 1 {
					changeSpec(concurrentStore, name, func(spec *api.DeviceSpec) { spec.Firmware = holdFirmwareSpec("firmware-repo") })
				}
			})

			status := updateRendered(name, "new-spec-hash")

			Expect(deviceservice.IsFirmwareHold(status)).To(BeTrue(), status.Message)
			Expect(calls).To(BeNumerically(">=", 2))
			after := getDevice(name)
			Expect(renderedStateOf(name)).To(Equal(before))
			Expect(after.Spec.Firmware).To(Equal(holdFirmwareSpec("firmware-repo")))
			expectHoldCondition(after)
		})

		It("When the outstanding attempt ends between the hold check and the write it should retry and render", func() {
			name := renderedDevice("race-ended")
			setFirmwareStatus(deviceStore, name, holdFirmwareStatus(api.FirmwareAttemptPhaseVerifying))
			calls := 0
			deviceStore.SetIntegrationTestCreateOrUpdateCallback(func() {
				calls++
				if calls == 1 {
					setFirmwareStatus(concurrentStore, name, holdFirmwareStatus(api.FirmwareAttemptPhaseSucceeded))
				}
			})

			status := updateRendered(name, "new-spec-hash")

			Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
			Expect(calls).To(BeNumerically(">=", 2))
			after := getDevice(name)
			Expect(renderedStateOf(name).Annotations).To(HaveKeyWithValue(api.DeviceAnnotationRenderedVersion, "2"))
			Expect(after.Status.Firmware).To(Equal(holdFirmwareStatus(api.FirmwareAttemptPhaseSucceeded)))
			expectSpecValid(after)
		})

		It("When every hold write loses its resource version race it should fail without writing", func() {
			name := renderedDevice("race-exhausted")
			setFirmwareStatus(deviceStore, name, holdFirmwareStatus(api.FirmwareAttemptPhaseStaging))
			before := getDevice(name)
			renderedBefore := renderedStateOf(name)
			calls := 0
			deviceStore.SetIntegrationTestCreateOrUpdateCallback(func() {
				calls++
				Expect(db.WithContext(ctx).Exec("UPDATE devices SET resource_version = resource_version + 1 WHERE org_id = ? AND name = ?", orgId, name).Error).To(Succeed())
			})

			status := updateRendered(name, "new-spec-hash")

			Expect(status.Code).ToNot(BeEquivalentTo(http.StatusOK))
			Expect(deviceservice.IsFirmwareHold(status)).To(BeFalse())
			Expect(calls).To(Equal(10))
			after := getDevice(name)
			Expect(renderedStateOf(name)).To(Equal(renderedBefore))
			Expect(after.Status.Conditions).To(Equal(before.Status.Conditions))
			Expect(firmwareJSON(after)).To(Equal(firmwareJSON(before)))
		})

		It("When firmware state appears before the unchanged render marks the spec valid it should hold instead", func() {
			name := renderedDevice("race-unchanged")
			specHash := renderedStateOf(name).Annotations[api.DeviceAnnotationRenderedSpecHash]
			Expect(deviceSvc.SetDeviceServiceConditions(ctx, orgId, name, []api.Condition{{
				Type: api.ConditionTypeDeviceSpecValid, Status: api.ConditionStatusFalse, Reason: "Invalid", Message: "earlier render failure",
			}}).Code).To(BeEquivalentTo(http.StatusOK))
			renderedBefore := renderedStateOf(name)
			eventsBefore := deviceEventReasons(name)
			calls := 0
			deviceStore.SetIntegrationTestCreateOrUpdateCallback(func() {
				calls++
				if calls == 1 {
					setFirmwareStatus(concurrentStore, name, holdFirmwareStatus(api.FirmwareAttemptPhaseAwaitingReboot))
				}
			})

			status := updateRendered(name, specHash)

			Expect(deviceservice.IsFirmwareHold(status)).To(BeTrue(), status.Message)
			Expect(calls).To(BeNumerically(">=", 2))
			after := getDevice(name)
			expectHoldCondition(after)
			Expect(renderedStateOf(name)).To(Equal(renderedBefore))
			Expect(deviceEventReasons(name)[len(eventsBefore):]).ToNot(ContainElement(string(api.EventReasonDeviceSpecValid)))
		})
	})

	Context("fleet rollout", func() {
		owner := lo.ToPtr("Fleet/" + fleetName)
		createFleet := func(tvName string, tvStatus api.TemplateVersionStatus) {
			testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
			Expect(testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, tvName, &tvStatus)).To(Succeed())
		}
		rollout := func(kind api.ResourceKind, name string) error {
			event := api.Event{Reason: api.EventReasonResourceUpdated, InvolvedObject: api.ObjectReference{Kind: string(kind), Name: name}}
			logic := tasks.NewFleetRolloutsLogic(log, fleetSvc, tvSvc, deviceSvc, dependencyrefSvc, orgId, event)
			if kind == api.FleetKind {
				return logic.RolloutFleet(ctx)
			}
			return logic.RolloutDevice(ctx)
		}
		expectHeldRollout := func(name string, before *api.Device) *api.Device {
			after := getDevice(name)
			Expect(specJSON(after)).To(Equal(specJSON(before)))
			Expect(firmwareJSON(after)).To(Equal(firmwareJSON(before)))
			Expect(after.Metadata.Generation).To(Equal(before.Metadata.Generation))
			Expect(lo.FromPtr(after.Metadata.Annotations)).ToNot(HaveKey(api.DeviceAnnotationTemplateVersion))
			expectHoldCondition(after)
			return after
		}

		It("When a device with desired firmware joins a fleet without firmware it should keep its spec and hold the rollout", func() {
			createFleet("1.0.0", api.TemplateVersionStatus{Os: &api.DeviceOsSpec{Image: "quay.io/example/os:2"}})
			name := uniqueName("joined")
			createDevice(name, owner, api.DeviceSpec{Config: inlineConfig("v1"), Firmware: holdFirmwareSpec("firmware-repo")})
			before := getDevice(name)

			Expect(rollout(api.DeviceKind, name)).To(Succeed())

			after := expectHeldRollout(name, before)
			Expect(after.Spec.Firmware).To(Equal(holdFirmwareSpec("firmware-repo")))

			By("rolling out again, nothing is written and no event is emitted")
			events := deviceEventReasons(name)
			Expect(rollout(api.DeviceKind, name)).To(Succeed())
			Expect(getDevice(name).Metadata.ResourceVersion).To(Equal(after.Metadata.ResourceVersion))
			Expect(deviceEventReasons(name)).To(Equal(events))
		})

		It("When a fleet device has an outstanding attempt it should hold that device and roll out the others", func() {
			createFleet("1.0.0", api.TemplateVersionStatus{Os: &api.DeviceOsSpec{Image: "quay.io/example/os:2"}})
			held := uniqueName("attempt")
			plain := uniqueName("plain")
			createDevice(held, owner, api.DeviceSpec{Config: inlineConfig("v1")})
			createDevice(plain, owner, api.DeviceSpec{Config: inlineConfig("v1")})
			setFirmwareStatus(deviceStore, held, holdFirmwareStatus(api.FirmwareAttemptPhaseWaitingForPolicy))
			before := getDevice(held)

			Expect(rollout(api.FleetKind, fleetName)).To(Succeed())

			expectHeldRollout(held, before)
			rolledOut := getDevice(plain)
			Expect(lo.FromPtr(rolledOut.Metadata.Annotations)).To(HaveKeyWithValue(api.DeviceAnnotationTemplateVersion, "1.0.0"))
			Expect(rolledOut.Spec.Os.Image).To(Equal("quay.io/example/os:2"))
		})

		It("When the template version carries firmware it should hold a device without firmware state", func() {
			createFleet("1.0.0", api.TemplateVersionStatus{Os: &api.DeviceOsSpec{Image: "quay.io/example/os:2"}, Firmware: holdFirmwareSpec("firmware-repo")})
			name := uniqueName("stale-template")
			createDevice(name, owner, api.DeviceSpec{Config: inlineConfig("v1")})
			before := getDevice(name)

			Expect(rollout(api.DeviceKind, name)).To(Succeed())

			expectHeldRollout(name, before)
		})

		It("When a fleet device has only a terminal attempt it should roll it out and keep the attempt", func() {
			createFleet("1.0.0", api.TemplateVersionStatus{Os: &api.DeviceOsSpec{Image: "quay.io/example/os:2"}})
			name := uniqueName("terminal")
			createDevice(name, owner, api.DeviceSpec{Config: inlineConfig("v1")})
			setFirmwareStatus(deviceStore, name, holdFirmwareStatus(api.FirmwareAttemptPhaseFailed))

			Expect(rollout(api.DeviceKind, name)).To(Succeed())

			after := getDevice(name)
			Expect(lo.FromPtr(after.Metadata.Annotations)).To(HaveKeyWithValue(api.DeviceAnnotationTemplateVersion, "1.0.0"))
			Expect(after.Status.Firmware).To(Equal(holdFirmwareStatus(api.FirmwareAttemptPhaseFailed)))
		})

		It("When an outstanding attempt is committed between the early check and the spec write it should retry and hold", func() {
			createFleet("1.0.0", api.TemplateVersionStatus{Os: &api.DeviceOsSpec{Image: "quay.io/example/os:2"}})
			name := uniqueName("race-attempt")
			createDevice(name, owner, api.DeviceSpec{Config: inlineConfig("v1")})
			before := getDevice(name)
			calls := 0
			deviceStore.SetIntegrationTestCreateOrUpdateCallback(func() {
				calls++
				if calls == 1 {
					setFirmwareStatus(concurrentStore, name, holdFirmwareStatus(api.FirmwareAttemptPhaseStaging))
				}
			})

			Expect(rollout(api.DeviceKind, name)).To(Succeed())

			Expect(calls).To(BeNumerically(">=", 2))
			after := getDevice(name)
			Expect(specJSON(after)).To(Equal(specJSON(before)))
			Expect(lo.FromPtr(after.Metadata.Annotations)).ToNot(HaveKey(api.DeviceAnnotationTemplateVersion))
			Expect(after.Status.Firmware).To(Equal(holdFirmwareStatus(api.FirmwareAttemptPhaseStaging)))
			expectHoldCondition(after)
		})

		It("When desired firmware is committed between the early check and the spec write it should retry and keep the firmware", func() {
			createFleet("1.0.0", api.TemplateVersionStatus{Os: &api.DeviceOsSpec{Image: "quay.io/example/os:2"}})
			name := uniqueName("race-spec")
			createDevice(name, owner, api.DeviceSpec{Config: inlineConfig("v1")})
			calls := 0
			deviceStore.SetIntegrationTestCreateOrUpdateCallback(func() {
				calls++
				if calls == 1 {
					changeSpec(concurrentStore, name, func(spec *api.DeviceSpec) { spec.Firmware = holdFirmwareSpec("firmware-repo") })
				}
			})

			Expect(rollout(api.DeviceKind, name)).To(Succeed())

			Expect(calls).To(BeNumerically(">=", 2))
			after := getDevice(name)
			Expect(after.Spec.Firmware).To(Equal(holdFirmwareSpec("firmware-repo")))
			Expect(decodedJSON(after.Spec.Config)).To(Equal(decodedJSON(inlineConfig("v1"))))
			Expect(lo.FromPtr(after.Metadata.Annotations)).ToNot(HaveKey(api.DeviceAnnotationTemplateVersion))
			expectHoldCondition(after)
		})
	})
})
