package tasks_test

import (
	"context"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
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

var _ = Describe("Fleet template firmware hold", func() {
	const fleetName = "firmware-fleet"
	var (
		log           *logrus.Logger
		ctx           context.Context
		orgId         uuid.UUID
		cfg           *config.Config
		dbName        string
		db            *gorm.DB
		fleetStore    fleetstore.Store
		tvStore       templateversionstore.Store
		eventsSvc     events.Service
		tvSvc         templateversionservice.Service
		deviceSvc     deviceservice.Service
		repositorySvc repositoryservice.Service
		workerClient  worker_client.WorkerClient
		payloads      [][]byte
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		tvStore = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		repoStore := repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		payloads = nil
		producer := queues.NewMockQueueProducer(gomock.NewController(GinkgoT()))
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, payload []byte, _ int64) error {
			payloads = append(payloads, append([]byte(nil), payload...))
			return nil
		}).AnyTimes()
		workerClient = worker_client.NewWorkerClient(producer, log, worker_client.WithDeltaPublisher(producer))
		kvStore, err := kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())
		DeferCleanup(kvStore.Close)
		eventsSvc = events.NewServiceHandler(eventstore.NewEventStore(db, log.WithField("pkg", "event-store")), workerClient, log)
		tvSvc = templateversionservice.NewServiceHandler(tvStore, kvStore, eventsSvc, log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store")), nil, fleetStore, eventsSvc, kvStore, "", log)
		repositorySvc = repositoryservice.NewServiceHandler(repoStore, eventsSvc, log)
		for _, name := range []string{"config-repo", "firmware-repo"} {
			spec := api.RepositorySpec{}
			Expect(spec.FromGitRepoSpec(api.GitRepoSpec{Url: "https://example.com/" + name, Type: api.GitRepoSpecTypeGit})).To(Succeed())
			_, err = repoStore.Create(ctx, orgId, &api.Repository{Metadata: api.ObjectMeta{Name: lo.ToPtr(name)}, Spec: spec})
			Expect(err).ToNot(HaveOccurred())
		}
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	templateSpec := func(firmware *api.FirmwareSpec) api.DeviceSpec {
		gitConfig := api.GitConfigProviderSpec{Name: "git-config"}
		gitConfig.GitRef.Repository = "config-repo"
		gitConfig.GitRef.Path = "/etc/app"
		gitConfig.GitRef.TargetRevision = "main"
		item := api.ConfigProviderSpec{}
		Expect(item.FromGitConfigProviderSpec(gitConfig)).To(Succeed())
		return api.DeviceSpec{
			Os:       &api.DeviceOsSpec{Image: "quay.io/example/os:1"},
			Config:   &[]api.ConfigProviderSpec{item},
			Firmware: firmware,
		}
	}
	createFleet := func(spec api.DeviceSpec) {
		fleet := &api.Fleet{Metadata: api.ObjectMeta{Name: lo.ToPtr(fleetName)}}
		fleet.Spec.Template.Spec = spec
		_, err := fleetStore.Create(ctx, orgId, fleet)
		Expect(err).ToNot(HaveOccurred())
	}
	replaceTemplate := func(spec api.DeviceSpec) {
		_, _, _, err := fleetStore.Mutate(ctx, orgId, fleetName, nil, func(m *fleetstore.FleetMutation) error {
			if err := m.RequireExisting(); err != nil {
				return err
			}
			m.Fleet.Spec.Template.Spec = spec
			return nil
		})
		Expect(err).ToNot(HaveOccurred())
	}
	validate := func(fleetSvc fleetservice.Service) error {
		event := api.Event{
			Reason:         api.EventReasonResourceUpdated,
			InvolvedObject: api.ObjectReference{Kind: api.FleetKind, Name: fleetName},
		}
		logic := tasks.NewFleetValidateLogic(log, fleetSvc, tvSvc, deviceSvc, repositorySvc, nil, orgId, event)
		logic.WorkerClient = workerClient
		return logic.CreateNewTemplateVersionIfFleetValid(ctx)
	}
	templateVersions := func() []api.TemplateVersion {
		list, err := tvStore.List(ctx, orgId, store.ListParams{})
		Expect(err).ToNot(HaveOccurred())
		return list.Items
	}
	repositoryRefs := func() []string {
		refs, err := fleetStore.GetRepositoryRefs(ctx, orgId, fleetName)
		Expect(err).ToNot(HaveOccurred())
		names := lo.Map(refs.Items, func(r api.Repository, _ int) string { return lo.FromPtr(r.Metadata.Name) })
		return names
	}
	storedFleet := func() *api.Fleet {
		fleet, err := fleetStore.Get(ctx, orgId, fleetName)
		Expect(err).ToNot(HaveOccurred())
		return fleet
	}
	expectFirmwareHold := func(fleet *api.Fleet) *api.Condition {
		condition := conditionOf(fleet.Status.Conditions, api.ConditionTypeFleetValid)
		Expect(condition).ToNot(BeNil())
		Expect(condition.Status).To(Equal(api.ConditionStatusFalse))
		Expect(condition.Reason).To(Equal(api.FleetValidReasonFirmwareRenderingUnsupported))
		annotations := lo.FromPtr(fleet.Metadata.Annotations)
		Expect(annotations).ToNot(HaveKey(api.FleetAnnotationTemplateVersion))
		Expect(annotations).ToNot(HaveKey(api.FleetAnnotationDeltaPrepareResourceVersion))
		Expect(annotations).ToNot(HaveKey(api.FleetAnnotationDeltaPrepareGeneration))
		return condition
	}

	It("When the fleet template carries firmware it should hold the fleet without a template version or rollout and keep its repository references current", func() {
		fleetSvc := fleetservice.NewServiceHandler(fleetStore, nil, eventsSvc, log, fleetservice.WithApprovedDescriptorProvider(holdDescriptors{}))
		createFleet(templateSpec(holdFirmwareSpec("firmware-repo")))

		Expect(validate(fleetSvc)).To(Succeed())

		Expect(templateVersions()).To(BeEmpty())
		expectFirmwareHold(storedFleet())
		Expect(queuedEventReasons(payloads)).ToNot(ContainElement(api.EventReasonPrepareDeltas))
		Expect(repositoryRefs()).To(ConsistOf("config-repo", "firmware-repo"))

		By("validating again, the hold is unchanged and still nothing is rolled out")
		before := storedFleet()
		Expect(validate(fleetSvc)).To(Succeed())
		after := storedFleet()
		Expect(templateVersions()).To(BeEmpty())
		Expect(conditionOf(after.Status.Conditions, api.ConditionTypeFleetValid)).To(Equal(conditionOf(before.Status.Conditions, api.ConditionTypeFleetValid)))
		Expect(queuedEventReasons(payloads)).ToNot(ContainElement(api.EventReasonPrepareDeltas))
	})

	It("When the firmware references of the fleet template are not known yet it should hold the fleet, keep the stored references and ask for a retry", func() {
		fleetSvc := fleetservice.NewServiceHandler(fleetStore, nil, eventsSvc, log)
		createFleet(templateSpec(nil))
		Expect(validate(fleetSvc)).To(Succeed())
		Expect(templateVersions()).To(HaveLen(1))
		Expect(repositoryRefs()).To(ConsistOf("config-repo"))
		payloads = nil

		replaceTemplate(templateSpec(holdFirmwareSpec("firmware-repo")))
		err := validate(fleetSvc)

		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("Retryable"))
		Expect(templateVersions()).To(HaveLen(1), "no template version may be created from the firmware template")
		condition := conditionOf(storedFleet().Status.Conditions, api.ConditionTypeFleetValid)
		Expect(condition).ToNot(BeNil())
		Expect(condition.Status).To(Equal(api.ConditionStatusFalse))
		Expect(condition.Reason).To(Equal(api.FleetValidReasonFirmwareRenderingUnsupported))
		Expect(condition.Message).To(ContainSubstring("firmware repository references"))
		Expect(repositoryRefs()).To(ConsistOf("config-repo"), "unknown firmware references must not replace the stored set")
		Expect(queuedEventReasons(payloads)).ToNot(ContainElement(api.EventReasonPrepareDeltas))
	})

	It("When the fleet template has no firmware it should create a template version and request its rollout", func() {
		fleetSvc := fleetservice.NewServiceHandler(fleetStore, nil, eventsSvc, log, fleetservice.WithApprovedDescriptorProvider(holdDescriptors{}))
		createFleet(templateSpec(nil))

		Expect(validate(fleetSvc)).To(Succeed())

		Expect(templateVersions()).To(HaveLen(1))
		condition := conditionOf(storedFleet().Status.Conditions, api.ConditionTypeFleetValid)
		Expect(condition).ToNot(BeNil())
		Expect(condition.Status).To(Equal(api.ConditionStatusTrue))
		Expect(queuedEventReasons(payloads)).To(ContainElement(api.EventReasonPrepareDeltas))
		Expect(repositoryRefs()).To(ConsistOf("config-repo"))
	})

	It("When firmware is removed from a held fleet template it should create a template version again", func() {
		fleetSvc := fleetservice.NewServiceHandler(fleetStore, nil, eventsSvc, log, fleetservice.WithApprovedDescriptorProvider(holdDescriptors{}))
		createFleet(templateSpec(holdFirmwareSpec("firmware-repo")))
		Expect(validate(fleetSvc)).To(Succeed())
		expectFirmwareHold(storedFleet())

		_, status := fleetSvc.PatchFleet(ctx, orgId, fleetName, api.PatchRequest{{Op: "remove", Path: "/spec/template/spec/firmware"}}, false)
		Expect(status.Code).To(BeEquivalentTo(200), status.Message)
		Expect(storedFleet().Spec.Template.Spec.Firmware).To(BeNil())

		Expect(validate(fleetSvc)).To(Succeed())

		Expect(templateVersions()).To(HaveLen(1))
		Expect(templateVersions()[0].Status.Firmware).To(BeNil())
		condition := conditionOf(storedFleet().Status.Conditions, api.ConditionTypeFleetValid)
		Expect(condition.Status).To(Equal(api.ConditionStatusTrue))
		Expect(repositoryRefs()).To(ConsistOf("config-repo"))
	})
})
