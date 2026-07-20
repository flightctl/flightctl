package tasks_test

import (
	"context"
	"fmt"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/internal/service/common"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
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

// This suite covers the dedicated application lifecycle APIs (stop/start/restart) end to end:
// they only ever set the device-controller/applicationLifecycle annotation, and the override is
// baked into RenderedApplications by the device render task, never into the device's persisted
// declarative spec (see internal/tasks/device_render.go).
var _ = Describe("Application lifecycle overlay at render time", func() {
	var (
		log                *logrus.Logger
		ctx                context.Context
		orgId              uuid.UUID
		deviceStore        devicestore.Store
		fleetStore         fleetstore.Store
		tvStore            templateversionstore.Store
		fleetSvc           fleetservice.Service
		templateVersionSvc templateversionservice.Service
		deviceSvc          deviceservice.Service
		dependencyrefSvc   dependencyrefservice.Service
		repositorySvc      repositoryservice.Service
		eventSvc           eventservice.Service
		cfg                *config.Config
		db                 *gorm.DB
		dbName             string
		fleetName          string
		deviceName         string
		workerClient       worker_client.WorkerClient
		mockQueueProducer  *queues.MockQueueProducer
		ctrl               *gomock.Controller
		kvStoreInst        kvstore.KVStore
		queuesProvider     queues.Provider
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		fleetName = "lifecycle-fleet"
		deviceName = "lifecycle-device-1"
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		tvStore = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		newDeviceStore := devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		newFleetStore := fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		newTvStore := templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		newRepoStore := repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		dependencyrefStore := dependencyrefstore.NewDependencyRefStore(db, log.WithField("pkg", "dependencyref-store"))
		eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		ctrl = gomock.NewController(GinkgoT())
		mockQueueProducer = queues.NewMockQueueProducer(ctrl)
		mockQueueProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		workerClient = worker_client.NewWorkerClient(mockQueueProducer, log)
		kvStoreInst, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())
		eventsSvc := events.NewServiceHandler(eventStore, workerClient, log)
		fleetSvc = fleetservice.NewServiceHandler(newFleetStore, eventsSvc, log)
		templateVersionSvc = templateversionservice.NewServiceHandler(newTvStore, kvStoreInst, eventsSvc, log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(newDeviceStore, newFleetStore, eventsSvc, kvStoreInst, "", log)
		dependencyrefSvc = dependencyrefservice.NewServiceHandler(dependencyrefStore, log)
		repositorySvc = repositoryservice.NewServiceHandler(newRepoStore, eventsSvc, log)
		eventSvc = eventservice.NewServiceHandler(eventStore, eventsSvc)

		// Initialize queues provider and rendered.Bus for successful device rendering.
		// Only initialize once (singleton pattern), subsequent calls are no-ops.
		if queuesProvider == nil {
			processID := fmt.Sprintf("device-render-lifecycle-test-%s", uuid.New().String())
			queuesProvider, err = queues.NewRedisProvider(ctx, log, processID, redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
			Expect(err).ToNot(HaveOccurred())
			err = rendered.Bus.Initialize(ctx, kvStoreInst, queuesProvider, 10*time.Second, log)
			Expect(err).ToNot(HaveOccurred())
		}
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
		ctrl.Finish()
	})

	containerAppTemplate := func(name string) api.ApplicationProviderSpec {
		containerApp := api.ContainerApplication{
			AppType: api.AppTypeContainer,
			Name:    lo.ToPtr(name),
			Image:   "quay.io/test/app:v1",
		}
		var app api.ApplicationProviderSpec
		Expect(app.FromContainerApplication(containerApp)).To(Succeed())
		return app
	}

	It("bakes a device's application lifecycle annotation into RenderedApplications without touching the persisted spec", func() {
		testutil.CreateTestFleet(ctx, fleetStore, orgId, fleetName, nil, nil)
		tvStatus := api.TemplateVersionStatus{
			Applications: &[]api.ApplicationProviderSpec{containerAppTemplate("app-1")},
		}
		Expect(testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", &tvStatus)).To(Succeed())
		testutil.CreateTestDevice(ctx, deviceStore, orgId, deviceName, lo.ToPtr("Fleet/"+fleetName), nil, nil)

		event := api.Event{
			Reason: api.EventReasonResourceUpdated,
			InvolvedObject: api.ObjectReference{
				Kind: api.DeviceKind,
				Name: deviceName,
			},
		}
		rolloutLogic := tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event)
		Expect(rolloutLogic.RolloutDevice(ctx)).To(Succeed())

		By("rendering the device for the first time with no lifecycle override")
		renderLogic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, event)
		Expect(renderLogic.RenderDevice(ctx)).To(Succeed())

		renderedDevice, status := deviceSvc.GetRenderedDevice(ctx, orgId, deviceName, api.GetRenderedDeviceParams{})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(renderedDevice.Spec.Applications).ToNot(BeNil())
		Expect(*renderedDevice.Spec.Applications).To(HaveLen(1))
		Expect((*renderedDevice.Spec.Applications)[0].GetDesiredState()).To(Equal(api.ApplicationDesiredStateRunning))

		By("stopping the application via the dedicated lifecycle API")
		_, stopStatus := deviceSvc.StopDeviceApplication(ctx, orgId, deviceName, "app-1")
		Expect(stopStatus.Code).To(Equal(int32(200)))

		By("the declarative spec is untouched by the stop API")
		dev, err := deviceStore.Get(ctx, orgId, deviceName)
		Expect(err).ToNot(HaveOccurred())
		Expect(dev.Spec.Applications).ToNot(BeNil())
		Expect((*dev.Spec.Applications)[0].GetDesiredState()).To(Equal(api.ApplicationDesiredStateRunning))

		By("re-rendering the device, the override should be baked into RenderedApplications only")
		lifecycleEvent := api.Event{
			Reason: api.EventReasonApplicationLifecycleChanged,
			InvolvedObject: api.ObjectReference{
				Kind: api.DeviceKind,
				Name: deviceName,
			},
		}
		renderLogic = tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, lifecycleEvent)
		Expect(renderLogic.RenderDevice(ctx)).To(Succeed())

		renderedDevice, status = deviceSvc.GetRenderedDevice(ctx, orgId, deviceName, api.GetRenderedDeviceParams{})
		Expect(status.Code).To(Equal(int32(200)))
		Expect(renderedDevice.Spec.Applications).ToNot(BeNil())
		Expect(*renderedDevice.Spec.Applications).To(HaveLen(1))
		Expect((*renderedDevice.Spec.Applications)[0].GetDesiredState()).To(Equal(api.ApplicationDesiredStateStopped))

		By("the persisted declarative spec still has no lifecycle fields after the override is baked in")
		dev, err = deviceStore.Get(ctx, orgId, deviceName)
		Expect(err).ToNot(HaveOccurred())
		Expect((*dev.Spec.Applications)[0].GetDesiredState()).To(Equal(api.ApplicationDesiredStateRunning))

		By("restarting the application increments restartGeneration atomically")
		_, restartStatus := deviceSvc.RestartDeviceApplication(ctx, orgId, deviceName, "app-1")
		Expect(restartStatus.Code).To(Equal(int32(200)))

		renderLogic = tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, lifecycleEvent)
		Expect(renderLogic.RenderDevice(ctx)).To(Succeed())

		renderedDevice, status = deviceSvc.GetRenderedDevice(ctx, orgId, deviceName, api.GetRenderedDeviceParams{})
		Expect(status.Code).To(Equal(int32(200)))
		Expect((*renderedDevice.Spec.Applications)[0].GetDesiredState()).To(Equal(api.ApplicationDesiredStateStopped),
			"restarting should not clear the stopped override")
		Expect((*renderedDevice.Spec.Applications)[0].GetRestartGeneration()).To(Equal(1))

		By("starting the application sets desiredState=running while preserving restartGeneration")
		_, startStatus := deviceSvc.StartDeviceApplication(ctx, orgId, deviceName, "app-1")
		Expect(startStatus.Code).To(Equal(int32(200)))

		renderLogic = tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, lifecycleEvent)
		Expect(renderLogic.RenderDevice(ctx)).To(Succeed())

		renderedDevice, status = deviceSvc.GetRenderedDevice(ctx, orgId, deviceName, api.GetRenderedDeviceParams{})
		Expect(status.Code).To(Equal(int32(200)))
		Expect((*renderedDevice.Spec.Applications)[0].GetDesiredState()).To(Equal(api.ApplicationDesiredStateRunning))
		Expect((*renderedDevice.Spec.Applications)[0].GetRestartGeneration()).To(Equal(1),
			"start no longer clears the annotation, so a previously-recorded restartGeneration must survive a stop/start cycle")

		By("restarting again after start simply continues incrementing restartGeneration")
		_, restartStatus = deviceSvc.RestartDeviceApplication(ctx, orgId, deviceName, "app-1")
		Expect(restartStatus.Code).To(Equal(int32(200)))

		renderLogic = tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, lifecycleEvent)
		Expect(renderLogic.RenderDevice(ctx)).To(Succeed())

		renderedDevice, status = deviceSvc.GetRenderedDevice(ctx, orgId, deviceName, api.GetRenderedDeviceParams{})
		Expect(status.Code).To(Equal(int32(200)))
		Expect((*renderedDevice.Spec.Applications)[0].GetRestartGeneration()).To(Equal(2))
	})

	It("propagates a fleet-level stop/start default to every member device, arbitrated by recency against each device's own override", func() {
		fleetApp := containerAppTemplate("app-1")

		// Unlike testutil.CreateTestFleet, this sets Spec.Template.Spec.Applications directly,
		// since StopFleetApplication/StartFleetApplication validate appName against the fleet's
		// own declarative template (mirroring how the device-scoped APIs validate against
		// device.Spec.Applications), not against the TemplateVersion.
		fleet := api.Fleet{Metadata: api.ObjectMeta{Name: lo.ToPtr(fleetName)}}
		fleet.Spec.Template.Spec.Applications = &[]api.ApplicationProviderSpec{fleetApp}
		noopCallback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
		_, err := fleetStore.Create(ctx, orgId, &fleet, noopCallback)
		Expect(err).ToNot(HaveOccurred())

		tvStatus := api.TemplateVersionStatus{
			Applications: &[]api.ApplicationProviderSpec{fleetApp},
		}
		Expect(testutil.CreateTestTemplateVersion(ctx, tvStore, orgId, fleetName, "1.0.0", &tvStatus)).To(Succeed())
		testutil.CreateTestDevice(ctx, deviceStore, orgId, deviceName, lo.ToPtr("Fleet/"+fleetName), nil, nil)

		rolloutEvent := func(name string) api.Event {
			return api.Event{
				Reason:         api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: name},
			}
		}
		lifecycleRenderEvent := func(name string) api.Event {
			return api.Event{
				Reason:         api.EventReasonApplicationLifecycleChanged,
				InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: name},
			}
		}
		renderDevice := func(event api.Event) {
			renderLogic := tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, &mockK8sClient{}, kvStoreInst, nil, orgId, event)
			Expect(renderLogic.RenderDevice(ctx)).To(Succeed())
		}
		rolloutAndRender := func(name string, event api.Event) {
			Expect(tasks.NewFleetRolloutsLogic(log, fleetSvc, templateVersionSvc, deviceSvc, dependencyrefSvc, orgId, event).RolloutDevice(ctx)).To(Succeed())
			renderDevice(event)
		}
		syncFleetFanOut := func(action api.ApplicationLifecycleChangedDetailsAction) {
			event := lo.FromPtr(common.GetFleetApplicationLifecycleChangedEvent(ctx, fleetName, "app-1", action))
			Expect(tasks.NewFleetApplicationLifecycleLogic(log, fleetSvc, deviceSvc, eventSvc, orgId, event).SyncFleet(ctx)).To(Succeed())
		}
		desiredStateOf := func(name string) api.ApplicationDesiredState {
			renderedDevice, status := deviceSvc.GetRenderedDevice(ctx, orgId, name, api.GetRenderedDeviceParams{})
			Expect(status.Code).To(Equal(int32(200)))
			Expect(*renderedDevice.Spec.Applications).To(HaveLen(1))
			return (*renderedDevice.Spec.Applications)[0].GetDesiredState()
		}
		deviceFleetCacheAnnotation := func(name string) string {
			dev, err := deviceStore.Get(ctx, orgId, name)
			Expect(err).ToNot(HaveOccurred())
			return lo.FromPtr(dev.Metadata.Annotations)[api.DeviceAnnotationFleetApplicationLifecycle]
		}

		By("rolling out and rendering device-1 before any fleet-level lifecycle action")
		rolloutAndRender(deviceName, rolloutEvent(deviceName))
		Expect(desiredStateOf(deviceName)).To(Equal(api.ApplicationDesiredStateRunning))

		By("a device-level stop overrides the fleet's (absent) default for that device only")
		_, deviceStopStatus := deviceSvc.StopDeviceApplication(ctx, orgId, deviceName, "app-1")
		Expect(deviceStopStatus.Code).To(Equal(int32(200)))
		renderDevice(lifecycleRenderEvent(deviceName))
		Expect(desiredStateOf(deviceName)).To(Equal(api.ApplicationDesiredStateStopped))

		By("starting the device again re-asserts running, since it is the most recent action for this device")
		_, deviceStartStatus := deviceSvc.StartDeviceApplication(ctx, orgId, deviceName, "app-1")
		Expect(deviceStartStatus.Code).To(Equal(int32(200)))
		renderDevice(lifecycleRenderEvent(deviceName))
		Expect(desiredStateOf(deviceName)).To(Equal(api.ApplicationDesiredStateRunning))

		By("stopping the application fleet-wide overrides the device's earlier start, since the fleet action happened more recently")
		_, stopStatus := fleetSvc.StopFleetApplication(ctx, orgId, fleetName, "app-1")
		Expect(stopStatus.Code).To(Equal(int32(200)))
		syncFleetFanOut(api.ApplicationLifecycleActionStop)
		renderDevice(lifecycleRenderEvent(deviceName))
		Expect(desiredStateOf(deviceName)).To(Equal(api.ApplicationDesiredStateStopped))

		By("a device-level start issued after that fleet-level stop wins again, since it is now the most recent action")
		_, startStatus := deviceSvc.StartDeviceApplication(ctx, orgId, deviceName, "app-1")
		Expect(startStatus.Code).To(Equal(int32(200)))
		renderDevice(lifecycleRenderEvent(deviceName))
		Expect(desiredStateOf(deviceName)).To(Equal(api.ApplicationDesiredStateRunning))

		By("stopping the application fleet-wide a second time wins yet again over the device's earlier start")
		_, stopAgainStatus := fleetSvc.StopFleetApplication(ctx, orgId, fleetName, "app-1")
		Expect(stopAgainStatus.Code).To(Equal(int32(200)))
		syncFleetFanOut(api.ApplicationLifecycleActionStop)
		renderDevice(lifecycleRenderEvent(deviceName))
		Expect(desiredStateOf(deviceName)).To(Equal(api.ApplicationDesiredStateStopped),
			"last-write-wins: the second fleet-level stop is more recent than the intervening device-level start")

		By("a device that joins the fleet later automatically inherits the fleet's current stop default")
		deviceName2 := "lifecycle-device-2"
		testutil.CreateTestDevice(ctx, deviceStore, orgId, deviceName2, lo.ToPtr("Fleet/"+fleetName), nil, nil)
		rolloutAndRender(deviceName2, rolloutEvent(deviceName2))
		Expect(desiredStateOf(deviceName2)).To(Equal(api.ApplicationDesiredStateStopped))
		bootstrappedCache := deviceFleetCacheAnnotation(deviceName2)
		Expect(bootstrappedCache).NotTo(BeEmpty())

		By("a routine (non-lifecycle) rollout for an already-bootstrapped device never re-syncs its fleet lifecycle cache")
		_, driftStatus := fleetSvc.StartFleetApplication(ctx, orgId, fleetName, "app-1")
		Expect(driftStatus.Code).To(Equal(int32(200)),
			"changes the fleet's own live annotation without running the fan-out task, simulating a fleet action whose fan-out hasn't reached this device yet")
		rolloutAndRender(deviceName2, rolloutEvent(deviceName2))
		Expect(deviceFleetCacheAnnotation(deviceName2)).To(Equal(bootstrappedCache),
			"a routine rollout must never resync an already-bootstrapped device's cache; only the explicit fan-out task may update it")
		Expect(desiredStateOf(deviceName2)).To(Equal(api.ApplicationDesiredStateStopped),
			"the device's render is unaffected by the fleet's own annotation change until the fan-out task actually runs")

		By("starting the application fleet-wide (with the fan-out task) brings every device without a newer override back to running")
		syncFleetFanOut(api.ApplicationLifecycleActionStart)
		renderDevice(lifecycleRenderEvent(deviceName2))
		Expect(desiredStateOf(deviceName2)).To(Equal(api.ApplicationDesiredStateRunning))
		renderDevice(lifecycleRenderEvent(deviceName))
		Expect(desiredStateOf(deviceName)).To(Equal(api.ApplicationDesiredStateRunning),
			"this fleet-wide start is more recent than device-1's own last override (an earlier start), so it takes effect too")
	})
})
