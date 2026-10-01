package tasks_test

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	labelsyncmappingservice "github.com/flightctl/flightctl/internal/service/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	labelsyncmappingstore "github.com/flightctl/flightctl/internal/store/labelsyncmapping"
	"github.com/flightctl/flightctl/internal/store/model"
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

type gatedLabelSyncEvaluator struct {
	inner       labelsyncmappingservice.Evaluator
	once        sync.Once
	started     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

func newGatedLabelSyncEvaluator(inner labelsyncmappingservice.Evaluator) *gatedLabelSyncEvaluator {
	return &gatedLabelSyncEvaluator{
		inner:   inner,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (e *gatedLabelSyncEvaluator) Evaluate(expression string, activation labelsyncmappingservice.Activation) (labelsyncmappingservice.Result, error) {
	e.once.Do(func() {
		close(e.started)
		<-e.release
	})
	return e.inner.Evaluate(expression, activation)
}

func (e *gatedLabelSyncEvaluator) ValidateExpressionIs(expression string, expected labelsyncmappingservice.ResultKind) error {
	return e.inner.ValidateExpressionIs(expression, expected)
}

func (e *gatedLabelSyncEvaluator) unblock() {
	e.releaseOnce.Do(func() { close(e.release) })
}

var _ = Describe("Device label reconciliation worker", func() {
	var (
		ctx          context.Context
		log          *logrus.Logger
		orgID        uuid.UUID
		cfg          *config.Config
		dbName       string
		db           *gorm.DB
		deviceStore  devicestore.Store
		fleetStore   fleetstore.Store
		eventStore   eventstore.Store
		mappingStore labelsyncmappingstore.Store
		deviceSvc    deviceservice.Service
		fleetSvc     fleetservice.Service
		mappingSvc   labelsyncmappingservice.Service
		eventsSvc    *events.ServiceHandler
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
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		eventStore = eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		mappingStore = labelsyncmappingstore.NewStore(db, log.WithField("pkg", "labelsyncmapping-store"))
		ctrl = gomock.NewController(GinkgoT())
		producer := queues.NewMockQueueProducer(ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		workerClient := worker_client.NewWorkerClient(producer, log)
		eventsSvc = events.NewServiceHandler(eventStore, workerClient, log)
		kvStoreInst, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).NotTo(HaveOccurred())
		deviceSvc = deviceservice.NewDeviceServiceHandler(deviceStore, nil, fleetStore, eventsSvc, kvStoreInst, "", log)
		fleetSvc = fleetservice.NewServiceHandler(fleetStore, nil, eventsSvc, log)
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

	newLabelSyncMappingService := func(evaluator labelsyncmappingservice.Evaluator) labelsyncmappingservice.Service {
		service, err := labelsyncmappingservice.NewServiceHandler(mappingStore, deviceStore, evaluator, eventsSvc, log)
		Expect(err).NotTo(HaveOccurred())
		return service
	}

	createMapping := func(name, key, expression string) *domain.LabelSyncMapping {
		mapping := domain.LabelSyncMapping{
			Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)},
			Spec: domain.LabelSyncMappingSpec{
				ResourceType: domain.LabelSyncMappingDevice,
				Key:          lo.ToPtr(key),
				Expression:   expression,
			},
		}
		created, status := mappingSvc.CreateLabelSyncMapping(ctx, orgID, mapping)
		Expect(status.Code).To(Equal(int32(201)), status.Message)
		return created
	}

	createDevice := func(name, architecture string, labels map[string]string, owner *string) *domain.Device {
		deviceStatus := domain.NewDeviceStatus()
		deviceStatus.SystemInfo.Architecture = architecture
		device := domain.Device{
			ApiVersion: domain.DeviceAPIVersion,
			Kind:       domain.DeviceKind,
			Metadata: domain.ObjectMeta{
				Name:   lo.ToPtr(name),
				Labels: &labels,
				Owner:  owner,
			},
			Spec:   &domain.DeviceSpec{},
			Status: &deviceStatus,
		}
		created, status := deviceSvc.CreateDevice(ctx, orgID, device)
		Expect(status.Code).To(Equal(int32(201)), status.Message)
		return created
	}

	systemInfoPatch := func(architecture string) domain.PatchRequest {
		info, err := util.StructToMap(domain.DeviceSystemInfo{Architecture: architecture})
		Expect(err).NotTo(HaveOccurred())
		var value interface{} = info
		return domain.PatchRequest{{Op: "replace", Path: "/status/systemInfo", Value: &value}}
	}

	patchArchitecture := func(name, architecture string) domain.Status {
		patch := systemInfoPatch(architecture)
		_, status := deviceSvc.PatchDeviceStatus(ctx, orgID, name, patch)
		return status
	}

	createEventForDevice := func(name string) domain.Event {
		list, err := eventStore.List(ctx, orgID, store.ListParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		for _, event := range list.Items {
			if event.InvolvedObject.Kind == domain.DeviceKind && event.InvolvedObject.Name == name && event.Reason == domain.EventReasonResourceCreated {
				return event
			}
		}
		Fail(fmt.Sprintf("ResourceCreated event for device %s was not persisted", name))
		return domain.Event{}
	}

	statusEventsForDevice := func(name string) []domain.Event {
		list, err := eventStore.List(ctx, orgID, store.ListParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		var result []domain.Event
		for _, event := range list.Items {
			if event.InvolvedObject.Kind == domain.DeviceKind && event.InvolvedObject.Name == name &&
				event.Reason == domain.EventReasonResourceUpdated && event.Details == nil {
				result = append(result, event)
			}
		}
		sort.Slice(result, func(i, j int) bool {
			return result[i].Metadata.CreationTimestamp.Before(*result[j].Metadata.CreationTimestamp)
		})
		return result
	}

	newWorkerLogic := func(service labelsyncmappingservice.Service, event domain.Event) tasks.DeviceLabelReconciliationLogic {
		logic, err := tasks.NewDeviceLabelReconciliationLogic(log, service, orgID, event)
		Expect(err).NotTo(HaveOccurred())
		return logic
	}

	readyCondition := func(mapping *domain.LabelSyncMapping) domain.Condition {
		Expect(mapping.Status).NotTo(BeNil())
		condition := domain.FindStatusCondition(lo.FromPtr(mapping.Status.Conditions), domain.ConditionTypeLabelSyncMappingReady)
		Expect(condition).NotTo(BeNil())
		return *condition
	}

	It("acknowledges stale identity-only work for a device that no longer exists", func() {
		event := domain.Event{
			Reason: domain.EventReasonResourceUpdated,
			InvolvedObject: domain.ObjectReference{
				Kind: domain.DeviceKind,
				Name: "deleted-device",
			},
		}
		Expect(newWorkerLogic(newLabelSyncMappingService(mustNewEvaluator()), event).Reconcile(ctx)).To(Succeed())
	})

	It("publishes post-commit identity events and reconciles reported labels into fleet membership", func() {
		name := "reported-status-device"
		createMapping("reported-architecture", "architecture", "status.systemInfo.architecture")
		selector := map[string]string{"architecture": "arm64"}
		testutil.CreateTestFleet(ctx, fleetStore, orgID, "arm64-fleet", &selector, nil)
		createDevice(name, "amd64", map[string]string{"site": "east"}, nil)

		createdEvent := createEventForDevice(name)
		Expect(createdEvent.Details).To(BeNil())
		Expect(newWorkerLogic(newLabelSyncMappingService(mustNewEvaluator()), createdEvent).Reconcile(ctx)).To(Succeed())

		status := patchArchitecture(name, "arm64")
		Expect(status.Code).To(Equal(int32(200)), status.Message)
		persisted, err := deviceStore.Get(ctx, orgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(persisted.Status.SystemInfo.Architecture).To(Equal("arm64"))
		Expect(lo.FromPtr(persisted.Metadata.Labels)).To(HaveKeyWithValue("architecture", "amd64"))
		statusEvents := statusEventsForDevice(name)
		Expect(statusEvents).NotTo(BeEmpty())
		statusEvent := statusEvents[len(statusEvents)-1]
		Expect(statusEvent.InvolvedObject.Kind).To(Equal(domain.DeviceKind))
		Expect(statusEvent.InvolvedObject.Name).To(Equal(name))
		Expect(statusEvent.Details).To(BeNil())

		var spoofedLabels interface{} = map[string]string{"architecture": "arm64", "site": "east"}
		spoofPatch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels", Value: &spoofedLabels}}
		_, spoofStatus := deviceSvc.PatchDeviceStatus(ctx, orgID, name, spoofPatch)
		Expect(spoofStatus.Code).To(Equal(int32(400)))
		var spoofedOwner interface{} = "Fleet/attacker"
		spoofOwnerPatch := domain.PatchRequest{{Op: "replace", Path: "/metadata/owner", Value: &spoofedOwner}}
		_, spoofOwnerStatus := deviceSvc.PatchDeviceStatus(ctx, orgID, name, spoofOwnerPatch)
		Expect(spoofOwnerStatus.Code).To(Equal(int32(400)))

		service := newLabelSyncMappingService(mustNewEvaluator())
		Expect(newWorkerLogic(service, statusEvent).Reconcile(ctx)).To(Succeed())
		persisted, err = deviceStore.Get(ctx, orgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(persisted.Metadata.Labels)).To(HaveKeyWithValue("architecture", "arm64"))
		Expect(lo.FromPtr(persisted.Metadata.Labels)).To(HaveKeyWithValue("site", "east"))
		Expect(persisted.Metadata.Owner).To(BeNil())

		list, err := eventStore.List(ctx, orgID, store.ListParams{Limit: 100})
		Expect(err).NotTo(HaveOccurred())
		var labelUpdateEvent *domain.Event
		for i := range list.Items {
			event := &list.Items[i]
			if event.InvolvedObject.Kind != domain.DeviceKind || event.InvolvedObject.Name != name || event.Reason != domain.EventReasonResourceUpdated || event.Details == nil {
				continue
			}
			details, detailsErr := event.Details.AsResourceUpdatedDetails()
			Expect(detailsErr).NotTo(HaveOccurred())
			if lo.Contains(details.UpdatedFields, domain.Labels) {
				labelUpdateEvent = event
				break
			}
		}
		Expect(labelUpdateEvent).NotTo(BeNil())
		selectorLogic := tasks.NewFleetSelectorMatchingLogic(log, deviceSvc, fleetSvc, orgID, *labelUpdateEvent)
		Expect(selectorLogic.DeviceLabelsUpdated(ctx)).To(Succeed())
		persisted, err = deviceStore.Get(ctx, orgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(persisted.Metadata.Owner)).To(Equal("Fleet/arm64-fleet"))
	})

	It("processes duplicated and out-of-order requests against the latest status and mapping", func() {
		name := "latest-state-device"
		mapping := createMapping("latest-state-mapping", "platform", "status.systemInfo.architecture")
		createDevice(name, "old", map[string]string{}, nil)
		createdEvent := createEventForDevice(name)
		Expect(patchArchitecture(name, "middle").Code).To(Equal(int32(200)))
		Expect(patchArchitecture(name, "latest").Code).To(Equal(int32(200)))

		mapping.Spec.Expression = `status.systemInfo.architecture == "latest" ? "current" : "stale"`
		_, updateStatus := mappingSvc.ReplaceLabelSyncMapping(ctx, orgID, "latest-state-mapping", *mapping)
		Expect(updateStatus.Code).To(Equal(int32(200)), updateStatus.Message)

		statusEvents := statusEventsForDevice(name)
		Expect(statusEvents).To(HaveLen(2))
		Expect(statusEvents[0].Metadata.CreationTimestamp.Before(*statusEvents[1].Metadata.CreationTimestamp)).To(BeTrue())
		service := newLabelSyncMappingService(mustNewEvaluator())
		Expect(newWorkerLogic(service, createdEvent).Reconcile(ctx)).To(Succeed())
		Expect(newWorkerLogic(service, statusEvents[1]).Reconcile(ctx)).To(Succeed())
		Expect(newWorkerLogic(service, statusEvents[0]).Reconcile(ctx)).To(Succeed())
		Expect(newWorkerLogic(service, statusEvents[1]).Reconcile(ctx)).To(Succeed())

		persisted, err := deviceStore.Get(ctx, orgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(persisted.Metadata.Labels)).To(HaveKeyWithValue("platform", "current"))
		Expect(lo.FromPtr(persisted.Metadata.Labels)).NotTo(HaveKey("stale"))
	})

	It("leaves mapping rollout state unchanged for per-device mapping errors", func() {
		name := "isolated-failure-device"
		good := createMapping("healthy-mapping", "architecture", "status.systemInfo.architecture")
		broken := createMapping("broken-mapping", "broken", "string(int(status.systemInfo.architecture))")
		createDevice(name, "amd64", map[string]string{}, nil)
		initialGood, status := mappingSvc.GetLabelSyncMapping(ctx, orgID, lo.FromPtr(good.Metadata.Name))
		Expect(status.Code).To(Equal(int32(200)))
		initialBroken, status := mappingSvc.GetLabelSyncMapping(ctx, orgID, lo.FromPtr(broken.Metadata.Name))
		Expect(status.Code).To(Equal(int32(200)))
		initialReadyConditions := map[string]domain.Condition{
			"healthy-mapping": readyCondition(initialGood),
			"broken-mapping":  readyCondition(initialBroken),
		}
		var initialMappingRows []model.LabelSyncMapping
		Expect(db.WithContext(ctx).Where("org_id = ?", orgID).Find(&initialMappingRows).Error).To(Succeed())
		initialFailureRevisions := make(map[string]int64, len(initialMappingRows))
		for _, row := range initialMappingRows {
			initialFailureRevisions[row.Name] = row.FailureRevision
		}

		service := newLabelSyncMappingService(mustNewEvaluator())
		Expect(newWorkerLogic(service, createEventForDevice(name)).Reconcile(ctx)).To(Succeed())

		good, status = mappingSvc.GetLabelSyncMapping(ctx, orgID, lo.FromPtr(good.Metadata.Name))
		Expect(status.Code).To(Equal(int32(200)))
		broken, status = mappingSvc.GetLabelSyncMapping(ctx, orgID, lo.FromPtr(broken.Metadata.Name))
		Expect(status.Code).To(Equal(int32(200)))
		Expect(readyCondition(good)).To(Equal(initialReadyConditions["healthy-mapping"]))
		Expect(readyCondition(broken)).To(Equal(initialReadyConditions["broken-mapping"]))

		var mappingRows []model.LabelSyncMapping
		Expect(db.WithContext(ctx).Where("org_id = ?", orgID).Find(&mappingRows).Error).To(Succeed())
		failureRevisions := make(map[string]int64, len(mappingRows))
		for _, row := range mappingRows {
			failureRevisions[row.Name] = row.FailureRevision
		}
		Expect(failureRevisions).To(Equal(initialFailureRevisions))
		persisted, err := deviceStore.Get(ctx, orgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(lo.FromPtr(persisted.Metadata.Labels)).To(HaveKeyWithValue("architecture", "amd64"))
	})

	It("keeps status processing independent while stale work retries after concurrent writes", func() {
		name := "concurrent-status-device"
		owner := "Fleet/assigned-fleet"
		createMapping("concurrent-mapping", "reported", "status.systemInfo.architecture")
		testutil.CreateTestFleet(ctx, fleetStore, orgID, "assigned-fleet", nil, nil)
		createDevice(name, "amd64", map[string]string{"site": "west"}, &owner)

		inner, err := labelsyncmappingservice.NewEvaluator()
		Expect(err).NotTo(HaveOccurred())
		gated := newGatedLabelSyncEvaluator(inner)
		service := newLabelSyncMappingService(gated)
		createdEvent := createEventForDevice(name)
		workerDone := make(chan struct{})
		workerErr := make(chan error, 1)
		go func() {
			workerErr <- newWorkerLogic(service, createdEvent).Reconcile(ctx)
			close(workerDone)
		}()
		defer func() {
			gated.unblock()
			select {
			case <-workerDone:
			case <-time.After(5 * time.Second):
				Fail("worker did not finish during cleanup")
			}
		}()
		select {
		case <-gated.started:
		case <-time.After(5 * time.Second):
			Fail("worker did not reach the CEL evaluator")
		}

		statusDone := make(chan domain.Status, 1)
		statusPatch := systemInfoPatch("arm64")
		go func() {
			_, status := deviceSvc.PatchDeviceStatus(ctx, orgID, name, statusPatch)
			statusDone <- status
		}()
		select {
		case status := <-statusDone:
			Expect(status.Code).To(Equal(int32(200)), status.Message)
		case <-time.After(5 * time.Second):
			Fail("status update waited for blocked worker evaluation")
		}
		for _, architecture := range []string{"riscv64", "arm64"} {
			Expect(patchArchitecture(name, architecture).Code).To(Equal(int32(200)))
		}
		Expect(statusEventsForDevice(name)).To(HaveLen(3))

		var operatorLabels interface{} = map[string]string{"site": "west", "operator": "kept"}
		labelPatch := domain.PatchRequest{{Op: "replace", Path: "/metadata/labels", Value: &operatorLabels}}
		_, patchStatus := deviceSvc.PatchDevice(ctx, orgID, name, labelPatch, true, true)
		Expect(patchStatus.Code).To(Equal(int32(200)), patchStatus.Message)

		mapping, mappingStatus := mappingSvc.GetLabelSyncMapping(ctx, orgID, "concurrent-mapping")
		Expect(mappingStatus.Code).To(Equal(int32(200)))
		mapping.Spec.Expression = `status.systemInfo.architecture == "arm64" ? "latest" : "stale"`
		_, mappingStatus = mappingSvc.ReplaceLabelSyncMapping(ctx, orgID, "concurrent-mapping", *mapping)
		Expect(mappingStatus.Code).To(Equal(int32(200)), mappingStatus.Message)
		gated.unblock()
		select {
		case <-workerDone:
			Expect(<-workerErr).NotTo(HaveOccurred())
		case <-time.After(5 * time.Second):
			Fail("worker did not finish after the evaluator was released")
		}

		persisted, err := deviceStore.Get(ctx, orgID, name)
		Expect(err).NotTo(HaveOccurred())
		Expect(persisted.Status.SystemInfo.Architecture).To(Equal("arm64"))
		labels := lo.FromPtr(persisted.Metadata.Labels)
		Expect(labels).To(HaveKeyWithValue("reported", "latest"))
		Expect(labels).To(HaveKeyWithValue("site", "west"))
		Expect(labels).To(HaveKeyWithValue("operator", "kept"))
		Expect(lo.FromPtr(persisted.Metadata.Owner)).To(Equal(owner))
	})
})

func mustNewEvaluator() labelsyncmappingservice.Evaluator {
	evaluator, err := labelsyncmappingservice.NewEvaluator()
	Expect(err).NotTo(HaveOccurred())
	return evaluator
}
