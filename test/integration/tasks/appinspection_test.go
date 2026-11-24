package tasks_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	inspectionservice "github.com/flightctl/flightctl/internal/service/appinspection"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/flightctl/flightctl/internal/store"
	inspectionstore "github.com/flightctl/flightctl/internal/store/appinspection"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/opencontainers/go-digest"
	"github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

type inspectionHelmRenderer func(context.Context) (string, error)

func (f inspectionHelmRenderer) RenderManifests(ctx context.Context, _ api.HelmApplication, _ *domain.Device, _ []byte, _ *domain.OciRepoSpec) (string, error) {
	return f(ctx)
}

type inspectionCommitHookStore struct {
	devicestore.Store
	afterCommit func()
}

func (s inspectionCommitHookStore) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	err := s.Store.WithTransaction(ctx, fn)
	if err == nil {
		s.afterCommit()
	}
	return err
}

type inspectionRetryEventStore struct {
	eventstore.Store
	failID string
	failed atomic.Bool
}

func (s *inspectionRetryEventStore) Create(ctx context.Context, orgID uuid.UUID, event *domain.Event) error {
	if lo.FromPtr(event.Metadata.Name) == s.failID && s.failed.CompareAndSwap(false, true) {
		return errors.New("retry event storage unavailable")
	}
	return s.Store.Create(ctx, orgID, event)
}

type inspectionRetryProducer struct {
	queues.QueueProducer
	failID string
	repair atomic.Bool
}

func (p *inspectionRetryProducer) Enqueue(ctx context.Context, body []byte, timestamp int64) error {
	var payload worker_client.EventWithOrgId
	if err := json.Unmarshal(body, &payload); err != nil {
		return err
	}
	if lo.FromPtr(payload.Event.Metadata.Name) == p.failID && !p.repair.Load() {
		return errors.New("retry event publication unavailable")
	}
	return p.QueueProducer.Enqueue(ctx, body, timestamp)
}

var _ = Describe("QueuedApplicationInspection", func() {
	orgID := store.NullOrgId
	var (
		ctx       context.Context
		log       *logrus.Logger
		cfg       *config.Config
		db        *gorm.DB
		storeSvc  *inspectionstore.Store
		devices   devicestore.Store
		deviceSvc deviceservice.Service
		repos     repositoryservice.Service
		kv        kvstore.KVStore
		origin    *tasks.DeviceRenderLogic
		provider  queues.Provider
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = logrus.New()
		var dbName string
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed()) })
		storeSvc, err = inspectionstore.NewStore(db)
		Expect(err).NotTo(HaveOccurred())
		provider, err = queues.NewRedisProvider(ctx, log, "inspection-test-"+uuid.NewString(), redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() { provider.Stop(); provider.Wait() })
		kv, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).NotTo(HaveOccurred())
		if rendered.Bus.Load() == nil {
			busProvider, err := queues.NewRedisProvider(ctx, log, "inspection-render-bus-"+uuid.NewString(), redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
			Expect(err).NotTo(HaveOccurred())
			Expect(rendered.Bus.Initialize(suiteCtx, kv, busProvider, time.Second, log)).To(Succeed())
		}
		eventsSvc := events.NewServiceHandler(eventstore.NewEventStore(db, log), nil, log)
		devices = devicestore.NewDeviceStore(db, log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(devices, nil, fleetstore.NewFleetStore(db, log), eventsSvc, kv, "", log, deviceservice.WithInspectionStore(storeSvc))
		repos = repositoryservice.NewServiceHandler(repositorystore.NewRepositoryStore(db, log), eventsSvc, log)
		origin = tasks.NewDeviceRenderLogic(log, deviceSvc, repos, nil, nil, kv, cfg)
	})

	createDeviceFor := func(spec string) *domain.Device {
		var app domain.ApplicationProviderSpec
		Expect(json.Unmarshal([]byte(spec), &app)).To(Succeed())
		device, err := devices.Create(ctx, orgID, &domain.Device{Metadata: domain.ObjectMeta{Name: lo.ToPtr("inspection-" + uuid.NewString())}, Spec: &domain.DeviceSpec{Applications: &[]domain.ApplicationProviderSpec{app}}}, nil)
		Expect(err).NotTo(HaveOccurred())
		return device
	}
	createDevice := func() *domain.Device {
		return createDeviceFor(`{"appType":"helm","name":"demo","image":"oci://registry.example/charts/app:v1"}`)
	}
	version := func(name string) string {
		device, status := deviceSvc.GetDevice(ctx, orgID, name)
		Expect(status.Code).To(BeEquivalentTo(200))
		return lo.FromPtr(device.Metadata.Annotations)[domain.DeviceAnnotationRenderedVersion]
	}
	specValidity := func(name string) *domain.Condition {
		device, status := deviceSvc.GetDevice(ctx, orgID, name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		return domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceSpecValid)
	}
	inspectResult := func(name string) inspectionstore.Result {
		page, err := storeSvc.ListUsages(ctx, orgID, inspectionstore.Owner{Kind: inspectionstore.OwnerDevice, Name: name}, 1, "")
		Expect(err).NotTo(HaveOccurred())
		Expect(page.Items).To(HaveLen(1))
		result, err := storeSvc.GetAttempt(ctx, orgID, page.Items[0].InspectionID)
		Expect(err).NotTo(HaveOccurred())
		return result
	}
	newCoordinator := func(renderer inspectionHelmRenderer) *inspectionservice.Coordinator {
		inspector, err := inspectionservice.NewInspector(repos)
		Expect(err).NotTo(HaveOccurred())
		resolver := inspectionservice.NewInputResolver(inspector, renderer, nil, cfg, log)
		return inspectionservice.NewCoordinator(storeSvc, resolver, 20*time.Second)
	}
	const fixtureManifest = "apiVersion: v1\nkind: Pod\nspec:\n  containers:\n  - name: web\n    image: registry.example/images/web:v1\n"
	current := func(name string) inspectionstore.OwnerProgress {
		progress, err := storeSvc.CurrentContinuation(ctx, orgID, inspectionstore.Owner{Kind: inspectionstore.OwnerDevice, Name: name})
		Expect(err).NotTo(HaveOccurred())
		return progress
	}
	disconnectedWakeups := func() *tasks.InspectionWakeups {
		ctrl := gomock.NewController(GinkgoT())
		provider := queues.NewMockProvider(ctrl)
		producer := queues.NewMockQueueProducer(ctrl)
		provider.EXPECT().NewQueueProducer(gomock.Any(), consts.ApplicationInspectionQueue).Return(producer, nil)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), queues.DurableWakeupTimestamp).Return(errors.New("queue unavailable")).AnyTimes()
		wakeups, err := tasks.NewInspectionWakeups(ctx, provider, storeSvc)
		Expect(err).NotTo(HaveOccurred())
		return wakeups
	}
	process := func(coordinator *inspectionservice.Coordinator, logic *tasks.DeviceRenderLogic) {
		ids, err := storeSvc.ListRecoverableWork(ctx, orgID, uuid.Nil, 64)
		Expect(err).NotTo(HaveOccurred())
		Expect(ids).NotTo(BeEmpty())
		for _, id := range ids {
			_, err := coordinator.Process(ctx, orgID, id, logic.ReloadInspectionApplication)
			Expect(err).NotTo(HaveOccurred())
		}
	}
	admit := func(logic *tasks.DeviceRenderLogic, name string, reason domain.EventReason) *domain.Event {
		event := domain.GetBaseEvent(ctx, domain.DeviceKind, name, reason, "", nil)
		Expect(logic.RenderDevice(ctx, orgID, *event)).To(Succeed())
		return event
	}

	It("When the queue handoff is interrupted it should recover without blocking ordinary work or publishing twice", func() {
		first := createDevice()
		firstName := lo.FromPtr(first.Metadata.Name)
		secondName := lo.FromPtr(createDevice().Metadata.Name)
		Expect(origin.RenderDevice(ctx, orgID, *domain.GetBaseEvent(ctx, domain.DeviceKind, firstName, domain.EventReasonResourceCreated, "", nil))).To(Succeed())
		previous := version(firstName)
		Expect(previous).NotTo(BeEmpty())
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		var renders atomic.Int32
		coordinator := newCoordinator(func(ctx context.Context) (string, error) {
			renders.Add(1)
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-release:
				return fixtureManifest, nil
			}
		})
		// Simulate a lost publication after durable intake.
		ctrl := gomock.NewController(GinkgoT())
		brokenProvider := queues.NewMockProvider(ctrl)
		brokenProducer := queues.NewMockQueueProducer(ctrl)
		brokenProvider.EXPECT().NewQueueProducer(gomock.Any(), consts.ApplicationInspectionQueue).Return(brokenProducer, nil)
		brokenProducer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), queues.DurableWakeupTimestamp).Return(errors.New("queue unavailable"))
		brokenWakeups, err := tasks.NewInspectionWakeups(ctx, brokenProvider, storeSvc)
		Expect(err).NotTo(HaveOccurred())
		firstEvent := domain.GetBaseEvent(ctx, domain.DeviceKind, firstName, domain.EventReasonDependencyChangeDetected, "", nil)
		Expect(origin.WithInspection(storeSvc, brokenWakeups, coordinator).RenderDevice(ctx, orgID, *firstEvent)).To(Succeed())
		Expect(version(firstName)).To(Equal(previous))
		firstProgress, err := storeSvc.CurrentContinuation(ctx, orgID, inspectionstore.Owner{Kind: inspectionstore.OwnerDevice, Name: firstName})
		Expect(err).NotTo(HaveOccurred())
		Expect(firstProgress.NeedsRetry).To(BeTrue())

		wakeups, err := tasks.NewInspectionWakeups(ctx, provider, storeSvc)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(wakeups.Close)
		logic := origin.WithInspection(storeSvc, wakeups, coordinator)
		workerCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		Expect(tasks.LaunchInspectionConsumer(workerCtx, provider, wakeups, coordinator, logic.ReloadInspectionApplication, logic.ResumeInspection, 5*time.Second)).To(Succeed())
		_, err = wakeups.Recover(ctx, orgID, tasks.InspectionRecoveryCursor{})
		Expect(err).NotTo(HaveOccurred())
		Eventually(entered, 5*time.Second).Should(Receive())
		// The regular queue can admit another render while Helm is blocked.
		Expect(tasks.LaunchConsumers(workerCtx, provider, tasks.TaskConsumer{DeviceRenderer: logic}, 1, 1)).To(Succeed())
		ordinary, err := provider.NewQueueProducer(ctx, consts.TaskQueue)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(ordinary.Close)
		worker_client.NewWorkerClient(ordinary, log).EmitEvent(ctx, orgID, domain.GetBaseEvent(ctx, domain.DeviceKind, secondName, domain.EventReasonDeviceConflictResolved, "", nil))
		Eventually(func(g Gomega) {
			progress, err := storeSvc.CurrentContinuation(ctx, orgID, inspectionstore.Owner{Kind: inspectionstore.OwnerDevice, Name: secondName})
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(progress.ID).NotTo(BeZero())
		}, 5*time.Second).Should(Succeed())
		Expect(version(firstName)).To(Equal(previous))
		Expect(version(secondName)).To(BeEmpty())
		close(release)
		Eventually(func() string { return version(secondName) }, 5*time.Second).ShouldNot(BeEmpty())
		Eventually(func() string { return version(firstName) }, 5*time.Second).ShouldNot(Equal(previous))
		Expect(renders.Load()).To(Equal(int32(1)))
		Expect(inspectResult(firstName).Inspection.ID).To(Equal(inspectResult(secondName).Inspection.ID))
		published := version(firstName)
		Expect(logic.ResumeInspection(ctx, orgID, firstProgress.ID)).To(Succeed())
		Expect(logic.RenderDevice(ctx, orgID, *firstEvent)).To(Succeed())
		Expect(version(firstName)).To(Equal(published))
	})

	DescribeTable("When a ready snapshot changes it should reconcile current input and preserve readiness on a failed write", func(preserveGeneration bool) {
		device := createDevice()
		name := lo.FromPtr(device.Metadata.Name)
		coordinator := newCoordinator(func(context.Context) (string, error) { return fixtureManifest, nil })
		logic := origin.WithInspection(storeSvc, disconnectedWakeups(), coordinator)
		admit(logic, name, domain.EventReasonResourceCreated)
		process(coordinator, logic)
		previous := current(name)
		Expect(storeSvc.ListReadyContinuations(ctx, orgID, uuid.Nil, 10)).To(Equal([]uuid.UUID{previous.ID}))
		Expect(version(name)).To(BeEmpty())

		updated, _, _, err := devices.Mutate(ctx, orgID, name, nil, func(m *devicestore.DeviceMutation) error {
			app, err := (*m.Device.Spec.Applications)[0].AsHelmApplication()
			if err != nil {
				return err
			}
			m.PreserveGeneration = preserveGeneration
			app.Values = lo.ToPtr(map[string]any{"changed": true})
			return (*m.Device.Spec.Applications)[0].FromHelmApplication(app)
		})
		Expect(err).NotTo(HaveOccurred())
		if preserveGeneration {
			Expect(updated.Metadata.Generation).To(Equal(device.Metadata.Generation))
		} else {
			Expect(lo.FromPtr(updated.Metadata.Generation)).To(BeNumerically(">", lo.FromPtr(device.Metadata.Generation)))
		}
		Expect(logic.ResumeInspection(ctx, orgID, previous.ID)).To(Succeed())
		Expect(version(name)).To(BeEmpty())
		next := current(name)
		Expect(next.ID).NotTo(Equal(previous.ID))
		Expect(next.Snapshot).NotTo(Equal(previous.Snapshot))
		process(coordinator, logic)

		status := deviceSvc.UpdateRenderedDevice(ctx, orgID, name, "", "[]", "invalid", "", nil, true, nil, deviceservice.RenderedDeviceOptions{
			Inspection: &deviceservice.RenderContinuation{ID: next.ID, Snapshot: next.Snapshot, ResourceVersion: "stale"},
		})
		Expect(status.Code).To(BeEquivalentTo(409))
		Expect(storeSvc.ListReadyContinuations(ctx, orgID, uuid.Nil, 10)).To(Equal([]uuid.UUID{next.ID}))
		Expect(version(name)).To(BeEmpty())

		// No completion wakeup was ever sent. Recovery publishes the ready ID.
		wakeups, err := tasks.NewInspectionWakeups(ctx, provider, storeSvc)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(wakeups.Close)
		workerCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		Expect(tasks.LaunchInspectionConsumer(workerCtx, provider, wakeups, coordinator, logic.ReloadInspectionApplication, logic.ResumeInspection, 5*time.Second)).To(Succeed())
		_, err = wakeups.Recover(ctx, orgID, tasks.InspectionRecoveryCursor{})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() string { return version(name) }, 5*time.Second).ShouldNot(BeEmpty())
		Eventually(func() *string { return current(name).PendingVersion }, 5*time.Second).Should(BeNil())
		Expect(current(name).ConsumedAt).NotTo(BeNil())

		// Restore the durable marker left by a crash between the rendered commit and notification.
		published := version(name)
		key := "v1/" + orgID.String() + "/device/" + name + "/rendered"
		Expect(kv.Delete(ctx, key)).To(Succeed())
		Expect(storeSvc.RecordPublication(ctx, orgID, next.ID, published)).To(Succeed())
		Expect(storeSvc.ListReadyContinuations(ctx, orgID, uuid.Nil, 10)).To(Equal([]uuid.UUID{next.ID}))
		_, err = wakeups.Recover(ctx, orgID, tasks.InspectionRecoveryCursor{})
		Expect(err).NotTo(HaveOccurred())
		Eventually(func() *string { return current(name).PendingVersion }, 5*time.Second).Should(BeNil())
		Expect(kv.Get(ctx, key)).To(Equal([]byte(published)))
		Expect(version(name)).To(Equal(published))
	}, Entry("with a same-generation input change", true), Entry("after the generation advances", false))

	It("When a no-change continuation commits it should leave a newer validity condition intact", func() {
		name := lo.FromPtr(createDevice().Metadata.Name)
		coordinator := newCoordinator(func(context.Context) (string, error) { return fixtureManifest, nil })
		logic := origin.WithInspection(storeSvc, disconnectedWakeups(), coordinator)
		admit(logic, name, domain.EventReasonResourceCreated)
		process(coordinator, logic)
		Expect(logic.ResumeInspection(ctx, orgID, current(name).ID)).To(Succeed())
		previous := version(name)
		admit(logic, name, domain.EventReasonDependencyChangeDetected)
		process(coordinator, logic)
		ready := current(name)
		invalid := domain.Condition{Type: domain.ConditionTypeDeviceSpecValid, Status: domain.ConditionStatusFalse, Reason: "Invalid", Message: "newer invalidation"}
		Expect(deviceSvc.SetDeviceServiceConditions(ctx, orgID, name, []domain.Condition{invalid}).Code).To(BeEquivalentTo(http.StatusOK))
		device, status := deviceSvc.GetDevice(ctx, orgID, name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		newer := invalid
		newer.Message = "concurrent invalidation"
		storeWithHook := inspectionCommitHookStore{devices, func() {
			Expect(specValidity(name)).To(HaveField("Status", domain.ConditionStatusTrue))
			Expect(deviceSvc.SetDeviceServiceConditions(ctx, orgID, name, []domain.Condition{newer}).Code).To(BeEquivalentTo(http.StatusOK))
		}}
		svc := deviceservice.NewDeviceServiceHandler(storeWithHook, nil, fleetstore.NewFleetStore(db, log), events.NewServiceHandler(eventstore.NewEventStore(db, log), nil, log), kv, "", log, deviceservice.WithInspectionStore(storeSvc))
		status = svc.UpdateRenderedDevice(ctx, orgID, name, "", "[]", lo.FromPtr(device.Metadata.Annotations)[domain.DeviceAnnotationRenderedSpecHash], "", nil, false, nil, deviceservice.RenderedDeviceOptions{
			Inspection: &deviceservice.RenderContinuation{ID: ready.ID, Snapshot: ready.Snapshot, ResourceVersion: lo.FromPtr(device.Metadata.ResourceVersion)},
		})
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
		Expect(specValidity(name)).To(HaveField("Message", newer.Message))
		Expect(version(name)).To(Equal(previous))
		Expect(current(name).ConsumedAt).NotTo(BeNil())
	})

	DescribeTable("When an interrupted render fails during resume it should settle only permanent failures on the current device", func(configJSON string, wantInvalid, retainReady, preserveGeneration bool) {
		name := lo.FromPtr(createDevice().Metadata.Name)
		admit(origin, name, domain.EventReasonResourceCreated)
		previous := version(name)
		coordinator := newCoordinator(func(context.Context) (string, error) { return fixtureManifest, nil })
		logic := origin.WithInspection(storeSvc, disconnectedWakeups(), coordinator)
		admit(logic, name, domain.EventReasonDependencyChangeDetected)
		process(coordinator, logic)
		ready := current(name)

		var configSpec domain.ConfigProviderSpec
		Expect(json.Unmarshal([]byte(configJSON), &configSpec)).To(Succeed())
		_, _, _, err := devices.Mutate(ctx, orgID, name, nil, func(m *devicestore.DeviceMutation) error {
			m.PreserveGeneration = preserveGeneration
			m.Device.Spec.Config = &[]domain.ConfigProviderSpec{configSpec}
			return nil
		})
		Expect(err).NotTo(HaveOccurred())
		if retainReady {
			Expect(logic.ResumeInspection(ctx, orgID, ready.ID)).NotTo(Succeed())
			Expect(storeSvc.ListReadyContinuations(ctx, orgID, uuid.Nil, 10)).To(ContainElement(ready.ID))
		} else {
			Expect(logic.ResumeInspection(ctx, orgID, ready.ID)).To(Succeed())
			Expect(storeSvc.ListReadyContinuations(ctx, orgID, uuid.Nil, 10)).NotTo(ContainElement(ready.ID))
			Expect(logic.ResumeInspection(ctx, orgID, ready.ID)).To(Succeed())
		}
		if wantInvalid {
			Expect(specValidity(name)).To(HaveField("Status", domain.ConditionStatusFalse))
			Expect(specValidity(name)).To(HaveField("Reason", "Invalid"))
		} else {
			Expect(specValidity(name)).To(HaveField("Status", domain.ConditionStatusTrue))
		}
		Expect(version(name)).To(Equal(previous))
	},
		Entry("permanent error", `{"name":"invalid","configType":"unknown"}`, true, false, true),
		Entry("transient error", `{"name":"http","configType":"HttpConfigProvider","httpRef":{"repository":"missing","filePath":"/etc/test"}}`, false, true, true),
		Entry("stale continuation", `{"name":"invalid","configType":"unknown"}`, false, false, false),
	)

	It("When Helm freshness expires it should refresh the child refs under the unchanged parent and keep older usages intact", func() {
		var renders int
		coordinator := newCoordinator(func(context.Context) (string, error) {
			renders++
			return fixtureManifest, nil
		})
		wakeups := disconnectedWakeups()
		logic := origin.WithInspection(storeSvc, wakeups, coordinator)
		first := lo.FromPtr(createDevice().Metadata.Name)
		admit(logic, first, domain.EventReasonResourceCreated)
		process(coordinator, logic)
		Expect(logic.ResumeInspection(ctx, orgID, current(first).ID)).To(Succeed())
		original := inspectResult(first)
		Expect(renders).To(Equal(1))
		Expect(original.Inspection.FreshUntil).NotTo(BeNil())

		var refreshes int
		// The restarted coordinator has an empty cache and the child now renders a new tag.
		coordinator = newCoordinator(func(context.Context) (string, error) {
			refreshes++
			return strings.ReplaceAll(fixtureManifest, "web:v1", "web:v2"), nil
		})
		logic = origin.WithInspection(storeSvc, wakeups, coordinator)
		second := lo.FromPtr(createDevice().Metadata.Name)
		admit(logic, second, domain.EventReasonResourceCreated)
		process(coordinator, logic)
		Expect(inspectResult(second).Inspection.ID).To(Equal(original.Inspection.ID))
		Expect(inspectResult(second).Inspection.FreshUntil).To(Equal(original.Inspection.FreshUntil))
		Expect(refreshes).To(BeZero())
		Expect(db.WithContext(ctx).Model(&model.ApplicationInspection{}).Where("org_id = ? AND id = ?", orgID, original.Inspection.ID).
			Update("fresh_until", gorm.Expr("clock_timestamp() - interval '1 second'")).Error).To(Succeed())
		third, fourth := lo.FromPtr(createDevice().Metadata.Name), lo.FromPtr(createDevice().Metadata.Name)
		admit(logic, third, domain.EventReasonResourceCreated)
		admit(logic, fourth, domain.EventReasonResourceCreated)
		process(coordinator, logic)
		refreshed := inspectResult(third)
		Expect(refreshes).To(Equal(1))
		Expect(refreshed.Inspection.ID).NotTo(Equal(original.Inspection.ID))
		Expect(refreshed.Inspection.Input).To(Equal(original.Inspection.Input))
		Expect(inspectResult(fourth).Inspection.ID).To(Equal(refreshed.Inspection.ID))
		Expect(refreshed.References).To(ContainElement(HaveField("Reference", "registry.example/images/web:v2")))
		Expect(inspectResult(first).References).To(ContainElement(HaveField("Reference", "registry.example/images/web:v1")))
	})

	It("When an OCI tag is resolved again it should reuse an unchanged digest and inspect a changed digest", func() {
		type artifact struct {
			blob, manifest []byte
			parent, layer  digest.Digest
		}
		bundle := func(tag string) artifact {
			blob := []byte("services:\n  web:\n    image: registry.example/images/web:" + tag + "\n")
			layer := digest.FromBytes(blob)
			manifest, err := json.Marshal(ocispec.Manifest{
				Versioned: specs.Versioned{SchemaVersion: 2}, Config: ocispec.DescriptorEmptyJSON,
				Layers: []ocispec.Descriptor{{MediaType: "application/yaml", Digest: layer, Size: int64(len(blob)),
					Annotations: map[string]string{ocispec.AnnotationTitle: "docker-compose.yaml"}}},
			})
			Expect(err).NotTo(HaveOccurred())
			return artifact{blob, manifest, digest.FromBytes(manifest), layer}
		}
		firstArtifact, secondArtifact := bundle("v1"), bundle("v2")
		var tagLookups, downloads atomic.Int32
		var retargeted atomic.Bool
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			path := r.URL.Path
			if path == "/v2/team/package/manifests/stable" {
				tagLookups.Add(1)
				target := firstArtifact
				if retargeted.Load() {
					target = secondArtifact
				}
				path = "/v2/team/package/manifests/" + target.parent.String()
			}
			for _, item := range []artifact{firstArtifact, secondArtifact} {
				switch path {
				case "/v2/team/package/manifests/" + item.parent.String():
					w.Header().Set("Content-Type", ocispec.MediaTypeImageManifest)
					w.Header().Set("Docker-Content-Digest", item.parent.String())
					w.Header().Set("Content-Length", strconv.Itoa(len(item.manifest)))
					if r.Method == http.MethodGet {
						// The client verifies the manifest against its digest.
						_, _ = w.Write(item.manifest)
					}
					return
				case "/v2/team/package/blobs/" + item.layer.String():
					downloads.Add(1)
					w.Header().Set("Content-Length", strconv.Itoa(len(item.blob)))
					// The client verifies the blob against its digest.
					_, _ = w.Write(item.blob)
					return
				}
			}
			http.NotFound(w, r)
		}))
		DeferCleanup(server.Close)
		host := strings.TrimPrefix(server.URL, "http://")
		repo := &domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr("package-registry")}}
		Expect(repo.Spec.FromOciRepoSpec(domain.OciRepoSpec{Type: domain.OciRepoSpecTypeOci, Registry: host, Scheme: lo.ToPtr(domain.OciRepoSchemeHttp)})).To(Succeed())
		Expect(repositorystore.NewRepositoryStore(db, log).Create(ctx, orgID, repo)).Error().NotTo(HaveOccurred())
		app := `{"appType":"compose","image":"` + host + `/team/package:stable"}`
		wakeups := disconnectedWakeups()
		first, second := lo.FromPtr(createDeviceFor(app).Metadata.Name), lo.FromPtr(createDeviceFor(app).Metadata.Name)
		for _, name := range []string{first, second} {
			// Each coordinator resolves through an empty digest cache.
			coordinator := newCoordinator(nil)
			logic := origin.WithInspection(storeSvc, wakeups, coordinator)
			admit(logic, name, domain.EventReasonResourceCreated)
			process(coordinator, logic)
		}
		original, reused := inspectResult(first), inspectResult(second)
		Expect(original.Inspection.State).To(Equal(model.InspectionComplete))
		Expect(original.Inspection.Input.ParentDigest).To(Equal(firstArtifact.parent))
		Expect(reused.Inspection.ID).To(Equal(original.Inspection.ID))
		Expect(tagLookups.Load()).To(Equal(int32(2)))
		Expect(downloads.Load()).To(Equal(int32(1)))

		retargeted.Store(true)
		coordinator := newCoordinator(nil)
		logic := origin.WithInspection(storeSvc, wakeups, coordinator)
		Expect(logic.ResumeInspection(ctx, orgID, current(first).ID)).To(Succeed())
		Expect(version(first)).To(BeEmpty())
		process(coordinator, logic)
		refreshed := inspectResult(first)
		Expect(refreshed.Inspection.Input.ParentDigest).To(Equal(secondArtifact.parent))
		Expect(refreshed.References).To(ContainElement(HaveField("Reference", "registry.example/images/web:v2")))
		Expect(inspectResult(second).Inspection.ID).To(Equal(original.Inspection.ID))
		Expect(downloads.Load()).To(Equal(int32(2)))
	})

	DescribeTable("When a live repository update repairs package access it should fence the old claim before retrying", func(spec domain.OciRepoSpec, image string) {
		repoName := "inspection-repair"
		repo := &domain.Repository{Metadata: domain.ObjectMeta{Name: &repoName}}
		Expect(repo.Spec.FromOciRepoSpec(spec)).To(Succeed())
		repoStore := repositorystore.NewRepositoryStore(db, log)
		Expect(repoStore.Create(ctx, orgID, repo)).Error().NotTo(HaveOccurred())

		name := lo.FromPtr(createDeviceFor(`{"appType":"compose","image":` + strconv.Quote(image) + `}`).Metadata.Name)
		logic := origin.WithInspection(storeSvc, disconnectedWakeups(), newCoordinator(nil))
		admit(logic, name, domain.EventReasonResourceCreated)
		before := current(name)
		work := inspectResult(name).Inspection.ID
		claim, err := storeSvc.Claim(ctx, orgID, work, time.Minute)
		Expect(err).NotTo(HaveOccurred())
		Expect(claim.Token).NotTo(BeZero())

		workerCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		ordinary, err := provider.NewQueueProducer(ctx, consts.TaskQueue)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(ordinary.Close)
		eventStore := eventstore.NewEventStore(db, log)
		emitter := events.NewServiceHandler(eventStore, worker_client.NewWorkerClient(ordinary, log), log)
		eventSvc := eventservice.NewServiceHandler(eventStore, emitter)
		repoSvc := repositoryservice.NewServiceHandler(repoStore, emitter, log)
		Expect(tasks.LaunchConsumers(workerCtx, provider, tasks.TaskConsumer{DeviceRenderer: logic, RepositorySvc: repoSvc, EventSvc: eventSvc}, 1, 1)).To(Succeed())

		spec.Scheme = lo.ToPtr(domain.OciRepoSchemeHttp)
		Expect(repo.Spec.FromOciRepoSpec(spec)).To(Succeed())
		_, status := repoSvc.ReplaceRepository(ctx, orgID, repoName, *repo)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		Eventually(func() uuid.UUID { return current(name).ID }, 5*time.Second).ShouldNot(Equal(before.ID))
		old, err := storeSvc.GetAttempt(ctx, orgID, work)
		Expect(err).NotTo(HaveOccurred())
		Expect(old.Inspection.ClaimToken).To(BeNil())
		Expect(storeSvc.ListRecoverableWork(ctx, orgID, uuid.Nil, 10)).To(HaveLen(1))
	},
		Entry("namespace", domain.OciRepoSpec{Type: domain.OciRepoSpecTypeOci, Registry: "registry.example", Namespace: lo.ToPtr("team")}, "registry.example/team/package:v1"),
		Entry("Docker Hub short repository", domain.OciRepoSpec{Type: domain.OciRepoSpecTypeOci, Registry: "docker.io", Repository: lo.ToPtr("nginx")}, "docker.io/library/nginx:v1"),
	)

	DescribeTable("When an inspection retry trigger cannot be delivered it should redeliver without duplicating stored events", Serial, func(failPersist bool) {
		repoName := "inspection-repair"
		repo := &domain.Repository{Metadata: domain.ObjectMeta{Name: &repoName}}
		Expect(repo.Spec.FromOciRepoSpec(domain.OciRepoSpec{
			Type: domain.OciRepoSpecTypeOci, Registry: "registry.example", Namespace: lo.ToPtr("team"),
		})).To(Succeed())
		repoStore := repositorystore.NewRepositoryStore(db, log)
		Expect(repoStore.Create(ctx, orgID, repo)).Error().NotTo(HaveOccurred())
		logic := origin.WithInspection(storeSvc, disconnectedWakeups(), newCoordinator(nil))
		names := []string{
			lo.FromPtr(createDeviceFor(`{"appType":"compose","image":"registry.example/team/package:v1"}`).Metadata.Name),
			lo.FromPtr(createDeviceFor(`{"appType":"compose","image":"registry.example/team/package:v1"}`).Metadata.Name),
		}
		slices.Sort(names)
		before := make([]uuid.UUID, len(names))
		for i, name := range names {
			admit(logic, name, domain.EventReasonResourceCreated)
			before[i] = current(name).ID
		}

		workerCtx, cancel := context.WithCancel(ctx)
		DeferCleanup(cancel)
		queue, err := provider.NewQueueProducer(ctx, consts.TaskQueue)
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(queue.Close)
		var details domain.EventDetails
		Expect(details.FromResourceUpdatedDetails(domain.ResourceUpdatedDetails{UpdatedFields: []domain.ResourceUpdatedDetailsUpdatedFields{domain.Spec}})).To(Succeed())
		event := domain.GetBaseEvent(ctx, domain.RepositoryKind, repoName, domain.EventReasonResourceUpdated, "", &details)
		eventID := uuid.MustParse(lo.FromPtr(event.Metadata.Name))
		triggerID := func(name string) string { return uuid.NewSHA1(eventID, []byte(name)).String() }
		retryStore := &inspectionRetryEventStore{Store: eventstore.NewEventStore(db, log)}
		retryQueue := &inspectionRetryProducer{QueueProducer: queue}
		if failPersist {
			retryStore.failID = triggerID(names[1])
		} else {
			retryQueue.failID = triggerID(names[1])
		}
		emitter := events.NewServiceHandler(retryStore, worker_client.NewWorkerClient(retryQueue, log), log)
		eventSvc := eventservice.NewServiceHandler(retryStore, emitter)
		repoSvc := repositoryservice.NewServiceHandler(repoStore, emitter, log)
		Expect(tasks.LaunchConsumers(workerCtx, provider, tasks.TaskConsumer{DeviceRenderer: logic, RepositorySvc: repoSvc, EventSvc: eventSvc}, 1, 1)).To(Succeed())
		eventSvc.CreateEvent(ctx, orgID, event)
		Expect(waitForRedisFailedMessagesState(ctx, consts.TaskQueue, 1, 5*time.Second)).To(BeTrue(), "Repository event should be eligible for redelivery")
		Eventually(func() uuid.UUID { return current(names[0]).ID }, 5*time.Second).ShouldNot(Equal(before[0]))
		Expect(current(names[1]).ID).To(Equal(before[1]))
		retryQueue.repair.Store(true)
		Eventually(func(g Gomega) {
			count, err := provider.RetryFailedMessages(ctx, consts.TaskQueue, queues.DefaultRetryConfig(), nil)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(count).To(Equal(1))
		}, 12*time.Second, 100*time.Millisecond).Should(Succeed())
		Eventually(func() uuid.UUID { return current(names[1]).ID }, 5*time.Second).ShouldNot(Equal(before[1]))
		for _, name := range names {
			list, status := eventSvc.ListEvents(ctx, orgID, domain.ListEventsParams{FieldSelector: lo.ToPtr("metadata.name=" + triggerID(name))})
			Expect(status.Code).To(BeEquivalentTo(http.StatusOK))
			Expect(list.Items).To(HaveLen(1))
		}
	}, Entry("event persistence fails", true), Entry("publication fails after persistence", false))

	DescribeTable("When inspection finishes incomplete it should publish best effort and retry only a distinct signal", func(retryReason domain.EventReason) {
		var accessible bool
		var renders int
		coordinator := newCoordinator(func(context.Context) (string, error) {
			renders++
			if !accessible {
				return "", errors.New("unavailable")
			}
			return fixtureManifest, nil
		})
		logic := origin.WithInspection(storeSvc, disconnectedWakeups(), coordinator)
		name := lo.FromPtr(createDevice().Metadata.Name)
		event := admit(logic, name, domain.EventReasonResourceCreated)
		process(coordinator, logic)
		attempt := inspectResult(name)
		Expect(attempt.Inspection.State).To(Equal(model.InspectionFinishedIncomplete))
		Expect(attempt.Inspection.DiagnosticCodes).To(ConsistOf(model.InspectionContentUnavailable))
		Expect(attempt.References).To(ContainElement(HaveField("Reference", "registry.example/charts/app:v1")))
		Expect(logic.ResumeInspection(ctx, orgID, current(name).ID)).To(Succeed())
		before := version(name)
		Expect(before).NotTo(BeEmpty())
		Expect(logic.RenderDevice(ctx, orgID, *event)).To(Succeed())
		Expect(storeSvc.ListRecoverableWork(ctx, orgID, uuid.Nil, 10)).To(BeEmpty())
		accessible = true
		admit(logic, name, retryReason)
		Expect(version(name)).To(Equal(before))
		device, status := deviceSvc.GetDevice(ctx, orgID, name)
		Expect(status.Code).To(BeEquivalentTo(200))
		condition := domain.FindStatusCondition(device.Status.Conditions, domain.ConditionTypeDeviceSpecValid)
		Expect(condition).NotTo(BeNil())
		Expect(condition.Status).To(Equal(domain.ConditionStatusTrue))
		process(coordinator, logic)
		Expect(inspectResult(name).Inspection.State).To(Equal(model.InspectionComplete))
		Expect(logic.ResumeInspection(ctx, orgID, current(name).ID)).To(Succeed())
		Expect(renders).To(Equal(2))
		Expect(version(name)).NotTo(Equal(before))
	}, Entry("a repository repair", domain.EventReasonReferencedRepositoryUpdated), Entry("a workload update", domain.EventReasonResourceUpdated))
})
