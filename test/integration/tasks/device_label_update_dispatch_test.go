package tasks_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	dependencyrefservice "github.com/flightctl/flightctl/internal/service/dependencyref"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
	dependencyrefstore "github.com/flightctl/flightctl/internal/store/dependencyref"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
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

var _ = Describe("Device label update dispatch", func() {
	var (
		ctx                context.Context
		log                *logrus.Logger
		orgID              uuid.UUID
		cfg                *config.Config
		dbName             string
		db                 *gorm.DB
		deviceStore        devicestore.Store
		fleetStore         fleetstore.Store
		templateVersionDB  templateversionstore.Store
		eventStore         eventstore.Store
		mappingStore       labelsyncmappingstore.Store
		deviceSvc          deviceservice.Service
		fleetSvc           fleetservice.Service
		templateVersionSvc templateversionservice.Service
		dependencyrefSvc   dependencyrefservice.Service
		mappingSvc         labelsyncmappingservice.Service
		eventSvc           *events.ServiceHandler
		taskEventSvc       eventservice.Service
		taskConsumer       tasks.TaskConsumer
		kvStoreInst        kvstore.KVStore
		ctrl               *gomock.Controller
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
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		templateVersionDB = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		eventStore = eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		mappingStore = labelsyncmappingstore.NewStore(db, log.WithField("pkg", "labelsyncmapping-store"))
		dependencyrefDB := dependencyrefstore.NewDependencyRefStore(db, log.WithField("pkg", "dependencyref-store"))

		ctrl = gomock.NewController(GinkgoT())
		producer := queues.NewMockQueueProducer(ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		workerClient := worker_client.NewWorkerClient(producer, log)
		eventSvc = events.NewServiceHandler(eventStore, workerClient, log)
		taskEventSvc = eventservice.NewServiceHandler(eventStore, eventSvc)
		kvStoreInst, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).NotTo(HaveOccurred())

		deviceSvc = deviceservice.NewDeviceServiceHandler(deviceStore, nil, fleetStore, eventSvc, kvStoreInst, "", log)
		fleetSvc = fleetservice.NewServiceHandler(fleetStore, nil, eventSvc, log)
		templateVersionSvc = templateversionservice.NewServiceHandler(templateVersionDB, kvStoreInst, eventSvc, log)
		dependencyrefSvc = dependencyrefservice.NewServiceHandler(dependencyrefDB, log)
		mappingSvc, err = labelsyncmappingservice.NewServiceHandler(mappingStore, deviceStore, mustNewEvaluator(), eventSvc, log)
		Expect(err).NotTo(HaveOccurred())
		taskConsumer = tasks.TaskConsumer{
			FleetSvc:            fleetSvc,
			TemplateversionSvc:  templateVersionSvc,
			DeviceSvc:           deviceSvc,
			DependencyrefSvc:    dependencyrefSvc,
			EventSvc:            taskEventSvc,
			LabelSyncMappingSvc: mappingSvc,
		}
	})

	AfterEach(func() {
		if kvStoreInst != nil {
			kvStoreInst.Close()
		}
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
		ctrl.Finish()
	})

	It("settles ownership before rolling out for manual label updates", func() {
		scenarios := []struct {
			name             string
			oldSelector      map[string]string
			newSelector      map[string]string
			labels           map[string]string
			expectedOwner    string
			expectedMultiple bool
			expectedTemplate string
		}{
			{
				name:             "switch-to-one-fleet",
				oldSelector:      map[string]string{"pool": "old"},
				newSelector:      map[string]string{"pool": "new"},
				labels:           map[string]string{"pool": "new"},
				expectedOwner:    "Fleet/new-switch-to-one-fleet",
				expectedTemplate: "2.0.0",
			},
			{
				name:             "lose-all-matches",
				oldSelector:      map[string]string{"pool": "old"},
				newSelector:      map[string]string{"pool": "new"},
				labels:           map[string]string{"pool": "none"},
				expectedTemplate: "0.9.0",
			},
			{
				name:             "match-multiple-fleets",
				oldSelector:      map[string]string{"track": "shared"},
				newSelector:      map[string]string{"track": "shared"},
				labels:           map[string]string{"track": "shared"},
				expectedOwner:    "Fleet/old-match-multiple-fleets",
				expectedMultiple: true,
				expectedTemplate: "0.9.0",
			},
			{
				name:             "change-unrelated-label",
				oldSelector:      map[string]string{"pool": "stable"},
				newSelector:      map[string]string{"pool": "other"},
				labels:           map[string]string{"pool": "stable", "site": "east"},
				expectedOwner:    "Fleet/old-change-unrelated-label",
				expectedTemplate: "1.0.0",
			},
		}

		for _, scenario := range scenarios {
			By("dispatching " + scenario.name)
			oldFleet := "old-" + scenario.name
			newFleet := "new-" + scenario.name
			deviceName := "device-" + scenario.name
			testutil.CreateTestFleet(ctx, fleetStore, orgID, oldFleet, &scenario.oldSelector, nil)
			testutil.CreateTestFleet(ctx, fleetStore, orgID, newFleet, &scenario.newSelector, nil)
			Expect(testutil.CreateTestTemplateVersion(ctx, templateVersionDB, orgID, oldFleet, "1.0.0", nil)).To(Succeed())
			Expect(testutil.CreateTestTemplateVersion(ctx, templateVersionDB, orgID, newFleet, "2.0.0", nil)).To(Succeed())
			testutil.CreateTestDevice(ctx, deviceStore, orgID, deviceName, lo.ToPtr("Fleet/"+oldFleet), lo.ToPtr("0.9.0"), &scenario.labels)

			event := domain.Event{
				Reason: domain.EventReasonResourceUpdated,
				InvolvedObject: domain.ObjectReference{
					Kind: domain.DeviceKind,
					Name: deviceName,
				},
			}
			details := domain.EventDetails{}
			Expect(details.FromResourceUpdatedDetails(domain.ResourceUpdatedDetails{
				UpdatedFields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.Labels},
			})).To(Succeed())
			event.Details = &details
			runDeviceTaskEvent(ctx, orgID, event, taskConsumer, ctrl)

			device, err := deviceStore.Get(ctx, orgID, deviceName)
			Expect(err).NotTo(HaveOccurred())
			Expect(lo.FromPtr(device.Metadata.Owner)).To(Equal(scenario.expectedOwner))
			condition := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceMultipleOwners)
			if scenario.expectedMultiple {
				Expect(condition).NotTo(BeNil())
				Expect(condition.Status).To(Equal(domain.ConditionStatusTrue))
			} else if condition != nil {
				Expect(condition.Status).To(Equal(domain.ConditionStatusFalse))
			}
			Expect(lo.FromPtr(device.Metadata.Annotations)[domain.DeviceAnnotationTemplateVersion]).To(Equal(scenario.expectedTemplate))
		}
	})

	It("dispatches promoted scalar and map labels after mapping reconciliation", func() {
		createMapping := func(name string, key *string, expression string) {
			mapping := domain.LabelSyncMapping{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
				Spec: domain.LabelSyncMappingSpec{
					ResourceType: domain.LabelSyncMappingDevice,
					Key:          key,
					Expression:   expression,
				},
			}
			_, status := mappingSvc.CreateLabelSyncMapping(ctx, orgID, mapping)
			Expect(status.Code).To(Equal(int32(201)), status.Message)
		}
		createMapping("promoted-architecture", lo.ToPtr("architecture"), "status.systemInfo.architecture")
		createMapping("promoted-site", nil, `{"site": status.systemInfo.customInfo["site"]}`)

		oldSelector := map[string]string{"architecture": "amd64", "site": "west"}
		newSelector := map[string]string{"architecture": "arm64", "site": "east"}
		testutil.CreateTestFleet(ctx, fleetStore, orgID, "old-promoted", &oldSelector, nil)
		testutil.CreateTestFleet(ctx, fleetStore, orgID, "new-promoted", &newSelector, nil)
		Expect(testutil.CreateTestTemplateVersion(ctx, templateVersionDB, orgID, "old-promoted", "1.0.0", nil)).To(Succeed())
		Expect(testutil.CreateTestTemplateVersion(ctx, templateVersionDB, orgID, "new-promoted", "2.0.0", nil)).To(Succeed())

		labels := map[string]string{"architecture": "amd64", "site": "west"}
		device := testutil.ReturnTestDevice(orgID, "promoted-device", lo.ToPtr("Fleet/old-promoted"), lo.ToPtr("0.9.0"), &labels)
		device.Status.SystemInfo.Architecture = "arm64"
		customInfo := domain.CustomDeviceInfo{"site": "east"}
		device.Status.SystemInfo.CustomInfo = &customInfo
		_, err := deviceStore.Create(ctx, orgID, &device, nil)
		Expect(err).NotTo(HaveOccurred())

		statusEvent := domain.Event{
			Reason: domain.EventReasonResourceUpdated,
			InvolvedObject: domain.ObjectReference{
				Kind: domain.DeviceKind,
				Name: "promoted-device",
			},
		}
		runDeviceTaskEvent(ctx, orgID, statusEvent, taskConsumer, ctrl)

		persistedDevice, err := deviceStore.Get(ctx, orgID, "promoted-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(persistedDevice.Metadata.Labels)).To(HaveKeyWithValue("architecture", "arm64"))
		Expect(lo.FromPtr(persistedDevice.Metadata.Labels)).To(HaveKeyWithValue("site", "east"))
		Expect(lo.FromPtr(persistedDevice.Metadata.Owner)).To(Equal("Fleet/old-promoted"))

		labelUpdateEvent := persistedDeviceLabelUpdateEvent(ctx, eventStore, orgID, "promoted-device")
		runDeviceTaskEvent(ctx, orgID, labelUpdateEvent, taskConsumer, ctrl)

		persistedDevice, err = deviceStore.Get(ctx, orgID, "promoted-device")
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(persistedDevice.Metadata.Owner)).To(Equal("Fleet/new-promoted"))
		Expect(lo.FromPtr(persistedDevice.Metadata.Annotations)[domain.DeviceAnnotationTemplateVersion]).To(Equal("2.0.0"))
	})
})

type oneEventTaskConsumer struct {
	payload       []byte
	processingErr error
	handlerErr    error
}

func (c *oneEventTaskConsumer) Consume(ctx context.Context, handler queues.ConsumeHandler) error {
	c.handlerErr = handler(ctx, c.payload, "entry-1", c, logrus.New())
	return nil
}

func (c *oneEventTaskConsumer) Complete(_ context.Context, _ string, _ []byte, processingErr error) error {
	c.processingErr = processingErr
	return nil
}

func (*oneEventTaskConsumer) Close() {}

func runDeviceTaskEvent(
	ctx context.Context,
	orgID uuid.UUID,
	event domain.Event,
	taskConsumer tasks.TaskConsumer,
	ctrl *gomock.Controller,
) {
	eventWithOrgID := worker_client.EventWithOrgId{OrgId: orgID, Event: event}
	payload, err := json.Marshal(eventWithOrgID)
	Expect(err).NotTo(HaveOccurred())
	consumer := &oneEventTaskConsumer{payload: payload}
	provider := queues.NewMockProvider(ctrl)
	provider.EXPECT().NewQueueConsumer(gomock.Any(), consts.TaskQueue).Return(consumer, nil).Times(1)
	Expect(tasks.LaunchConsumers(ctx, provider, taskConsumer, 1, 1)).To(Succeed())
	Expect(consumer.handlerErr).NotTo(HaveOccurred())
	Expect(consumer.processingErr).NotTo(HaveOccurred())
}

func persistedDeviceLabelUpdateEvent(ctx context.Context, events eventstore.Store, orgID uuid.UUID, deviceName string) domain.Event {
	list, err := events.List(ctx, orgID, store.ListParams{Limit: 100})
	Expect(err).NotTo(HaveOccurred())
	for _, event := range list.Items {
		if event.InvolvedObject.Kind != domain.DeviceKind || event.InvolvedObject.Name != deviceName ||
			event.Reason != domain.EventReasonResourceUpdated || event.Details == nil {
			continue
		}
		details, err := event.Details.AsResourceUpdatedDetails()
		Expect(err).NotTo(HaveOccurred())
		if lo.Contains(details.UpdatedFields, domain.Labels) {
			return event
		}
	}
	Fail(fmt.Sprintf("expected a persisted label update event for device %s", deviceName))
	return domain.Event{}
}
