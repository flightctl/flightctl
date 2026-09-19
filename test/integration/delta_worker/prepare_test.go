package delta_worker_test

import (
	"context"
	"encoding/json"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	deltaconfig "github.com/flightctl/flightctl/internal/delta_worker/config"
	"github.com/flightctl/flightctl/internal/delta_worker/model"
	workerservice "github.com/flightctl/flightctl/internal/delta_worker/service"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltageneration"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltaprepare"
	"github.com/flightctl/flightctl/internal/delta_worker/service/deltapreparegeneration"
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store/deltageneration"
	deltapreparestore "github.com/flightctl/flightctl/internal/delta_worker/store/deltaprepare"
	deltapreparegenerationstore "github.com/flightctl/flightctl/internal/delta_worker/store/deltapreparegeneration"
	generationcomplete "github.com/flightctl/flightctl/internal/delta_worker/tasks/generationcomplete"
	preparetask "github.com/flightctl/flightctl/internal/delta_worker/tasks/prepare"
	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	templateversionstore "github.com/flightctl/flightctl/internal/store/templateversion"
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

var _ = Describe("PrepareDeltas persist", func() {
	var (
		log                         *logrus.Logger
		ctx                         context.Context
		orgId                       uuid.UUID
		cfg                         *config.Config
		dbName                      string
		db                          *gorm.DB
		deltaPrepareStore           deltapreparestore.Store
		deltaGenerationStore        deltastore.Store
		deltaPrepareGenerationStore deltapreparegenerationstore.Store
		fleets                      *fleetstore.FleetStore
		devices                     *devicestore.DeviceStore
		repos                       repositorystore.Store
		templateVersions            templateversionstore.Store
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		generationStore := deltastore.NewStore(db, log.WithField("pkg", "delta-generation-store"))
		prepareStore := deltapreparestore.NewStore(db, log.WithField("pkg", "delta-prepare-store"))
		prepareGenerationStore := deltapreparegenerationstore.NewStore(db, log.WithField("pkg", "delta-prepare-generation-store"))
		Expect(generationStore.InitialMigration(ctx)).To(Succeed())
		Expect(prepareStore.InitialMigration(ctx)).To(Succeed())
		Expect(prepareGenerationStore.InitialMigration(ctx)).To(Succeed())
		deltaPrepareStore = prepareStore
		deltaGenerationStore = generationStore
		deltaPrepareGenerationStore = prepareGenerationStore
		fleets = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		devices = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		repos = repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		templateVersions = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		orgs := organizationstore.NewOrganizationStore(db)
		orgId = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, orgs, orgId)).To(Succeed())
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	When("two eligible devices share a digest pair", func() {
		It("should insert one generation and enqueue one GenerateDelta", func() {
			const (
				fleetName = "fleet-1"
				tvName    = "tv-1"
				newImage  = "quay.io/acme/os:v2"
				repoName  = "quay.io/acme/os"
				srcDigest = "sha256:aaa"
				tgtDigest = "sha256:bbb"
			)

			testutil.CreateTestFleet(ctx, fleets, orgId, fleetName, nil, nil)
			_, _, err := fleets.UpdateAnnotations(ctx, orgId, fleetName, map[string]string{
				domain.FleetAnnotationTemplateVersion: tvName,
			}, nil)
			Expect(err).ToNot(HaveOccurred())
			_, err = templateVersions.Create(ctx, orgId, &domain.TemplateVersion{
				Metadata: domain.ObjectMeta{
					Name:  lo.ToPtr(tvName),
					Owner: util.SetResourceOwner(domain.FleetKind, fleetName),
				},
				Spec:   domain.TemplateVersionSpec{Fleet: fleetName},
				Status: &domain.TemplateVersionStatus{Os: &domain.DeviceOsSpec{Image: newImage}},
			})
			Expect(err).ToNot(HaveOccurred())

			owner := util.SetResourceOwner(domain.FleetKind, fleetName)
			testutil.CreateTestDevices(ctx, 2, devices, orgId, owner, true)
			for _, name := range []string{"mydevice-1", "mydevice-2"} {
				device, err := devices.Get(ctx, orgId, name)
				Expect(err).ToNot(HaveOccurred())
				Expect(device.Status).ToNot(BeNil())
				device.Status.Os.ImageDigest = srcDigest
				device.Status.SystemInfo.DeltaEligible = lo.ToPtr(true)
				device.Status.SystemInfo.BootcVersion = lo.ToPtr("bootc 1.15.0")
				_, _, err = devices.UpdateStatus(ctx, orgId, device, nil)
				Expect(err).ToNot(HaveOccurred())
			}

			spec := domain.RepositorySpec{}
			Expect(spec.FromOciRepoSpec(domain.OciRepoSpec{
				Registry:           "my-registry.com",
				Type:               domain.OciRepoSpecTypeOci,
				Repository:         lo.ToPtr("my-org/diffs"),
				DeltaStorageTarget: lo.ToPtr(true),
			})).To(Succeed())
			_, err = repos.Create(ctx, orgId, &domain.Repository{
				ApiVersion: "v1beta1",
				Kind:       domain.RepositoryKind,
				Metadata:   domain.ObjectMeta{Name: lo.ToPtr("diffs")},
				Spec:       spec,
			})
			Expect(err).ToNot(HaveOccurred())

			emit := &prepareEmitSpy{}
			status := workerservice.NewStorePreparingStatus(fleets, devices)
			generationService := deltageneration.NewServiceHandler(deltaGenerationStore, log)
			prepareService := deltaprepare.NewServiceHandler(deltaPrepareStore, status)
			prepareGenerationService := deltapreparegeneration.NewServiceHandler(deltaPrepareGenerationStore, generationService, nil)
			fleetService := fleetservice.NewServiceHandler(fleets, nil, nil, log)
			deviceService := deviceservice.NewDeviceServiceHandler(devices, nil, fleets, nil, nil, "", log)
			repositoryService := repositoryservice.NewServiceHandler(repos, nil, log)
			templateVersionService := templateversionservice.NewServiceHandler(templateVersions, nil, nil, log)
			p, err := preparetask.NewHandler(
				&preparetask.Resolver{
					FleetService:           fleetService,
					DeviceService:          deviceService,
					RepositoryService:      repositoryService,
					TemplateVersionService: templateVersionService,
					Config:                 &deltaconfig.DeltaGenerationConfig{},
					Render: func(ctx context.Context, org uuid.UUID, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
						logic := tasks.NewDeviceRenderLogic(log, deviceService, repositoryService, nil, nil, nil, nil, org, domain.Event{})
						return logic.RenderSpec(ctx, spec)
					},
					Inspect: func(_ context.Context, _ uuid.UUID, image string) (string, error) {
						Expect(image).To(Equal(newImage))
						return tgtDigest, nil
					},
				},
				emit.emit,
				prepareService,
				generationService,
				prepareGenerationService,
			)
			Expect(err).ToNot(HaveOccurred())
			p.Now = time.Now
			p.DeltaGenerationTimeout = 30 * time.Minute

			Expect(p.Prepare(ctx, fleetPrepareEvent(orgId, fleetName, tvName))).To(Succeed())

			waiting, err := deltaPrepareStore.GetDeltaPrepare(ctx, deltapreparestore.PrepareKey{OrgID: orgId, Kind: domain.FleetKind, Name: fleetName}, deltapreparestore.WithPrepareStatus(model.DeltaPrepareWaiting))
			Expect(err).ToNot(HaveOccurred())
			Expect(waiting).ToNot(BeNil())
			Expect(waiting.Status).To(Equal(model.DeltaPrepareWaiting))

			gen, err := deltaGenerationStore.GetDeltaGeneration(ctx, deltastore.GenerationKey{
				OrgID:           orgId,
				ImageRepository: repoName,
				SourceDigest:    srcDigest,
				TargetDigest:    tgtDigest,
			})
			Expect(err).ToNot(HaveOccurred())
			Expect(gen).ToNot(BeNil())

			Expect(emit.reasons()).To(Equal([]domain.EventReason{domain.EventReasonGenerateDelta}))
			var payload map[string]string
			Expect(json.Unmarshal([]byte(emit.events[0].Message), &payload)).To(Succeed())
			Expect(payload["imageRepository"]).To(Equal(repoName))
			Expect(payload["sourceDigest"]).To(Equal(srcDigest))
			Expect(payload["targetDigest"]).To(Equal(tgtDigest))
		})
	})

	When("a prepare completion is newer than the Fleet preparing marker", func() {
		const (
			fleetName = "fleet-cleanup"
			tvName    = "tv-cleanup"
		)

		setPreparingMarker := func(sourceResourceVersion string) {
			testutil.CreateTestFleet(ctx, fleets, orgId, fleetName, nil, nil)
			_, _, _, err := fleets.Mutate(ctx, orgId, fleetName, nil, func(m *fleetstore.FleetMutation) error {
				annotations := map[string]string{
					domain.FleetAnnotationTemplateVersion:             tvName,
					domain.FleetAnnotationDeltaPrepareResourceVersion: sourceResourceVersion,
				}
				m.Fleet.Metadata.Annotations = &annotations
				m.Fleet.Status = &domain.FleetStatus{
					Conditions: []domain.Condition{{
						Type:   domain.ConditionTypeFleetDeltaPreparing,
						Status: domain.ConditionStatusTrue,
					}},
					DeltaGeneration: &domain.DeltaGenerationStatus{Completed: 0, Total: 1},
				}
				return nil
			})
			Expect(err).ToNot(HaveOccurred())
		}

		It("should clear an older marker", func() {
			setPreparingMarker("10")

			updated, err := fleets.ResumeDeltaIfCurrent(ctx, orgId, fleetName, tvName, 11)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).ToNot(BeNil())
			Expect(domain.FindStatusCondition(updated.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)).To(BeNil())
			Expect((*updated.Metadata.Annotations)[domain.FleetAnnotationDeltaPrepareResourceVersion]).To(BeEmpty())
		})

		It("should not clear a newer marker", func() {
			setPreparingMarker("12")

			updated, err := fleets.ResumeDeltaIfCurrent(ctx, orgId, fleetName, tvName, 11)
			Expect(err).ToNot(HaveOccurred())
			Expect(updated).To(BeNil())

			current, err := fleets.Get(ctx, orgId, fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(domain.FindStatusCondition(current.Status.Conditions, domain.ConditionTypeFleetDeltaPreparing)).ToNot(BeNil())
			Expect((*current.Metadata.Annotations)[domain.FleetAnnotationDeltaPrepareResourceVersion]).To(Equal("12"))
		})
	})

	When("the last joined generation pair becomes terminal", func() {
		It("should complete the waiting prepare and emit FleetRolloutStarted", func() {
			const (
				fleetName = "fleet-resume"
				tvName    = "tv-resume"
				repoName  = "quay.io/acme/os"
				srcDigest = "sha256:src"
				tgtDigest = "sha256:tgt"
			)

			testutil.CreateTestFleet(ctx, fleets, orgId, fleetName, nil, nil)
			_, _, err := fleets.UpdateAnnotations(ctx, orgId, fleetName, map[string]string{
				domain.FleetAnnotationTemplateVersion: tvName,
			}, nil)
			Expect(err).ToNot(HaveOccurred())
			tvs := templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "tv-store"))
			Expect(testutil.CreateTestTemplateVersion(ctx, tvs, orgId, fleetName, tvName, &domain.TemplateVersionStatus{
				Os: &domain.DeviceOsSpec{Image: "quay.io/acme/os:v2"},
			})).To(Succeed())

			ctrl := gomock.NewController(GinkgoT())
			DeferCleanup(ctrl.Finish)
			producer := queues.NewMockQueueProducer(ctrl)
			producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
			eventsSvc := events.NewServiceHandler(eventstore.NewEventStore(db, log.WithField("pkg", "event-store")), worker_client.NewWorkerClient(producer, log), log)
			key := deltastore.GenerationKey{
				OrgID:           orgId,
				ImageRepository: repoName,
				SourceDigest:    srcDigest,
				TargetDigest:    tgtDigest,
			}
			_, err = deltaGenerationStore.InsertDeltaGenerations(ctx, []*model.DeltaGeneration{{
				OrgID:           orgId,
				ImageRepository: repoName,
				SourceDigest:    srcDigest,
				TargetDigest:    tgtDigest,
			}})
			Expect(err).ToNot(HaveOccurred())
			tv := tvName
			prep := &model.DeltaPrepare{
				OrgID:           orgId,
				Kind:            domain.FleetKind,
				Name:            fleetName,
				TemplateVersion: &tv,
			}
			Expect(deltaPrepareStore.CreateDeltaPrepare(ctx, prep)).To(Succeed())
			_, err = deltaPrepareGenerationStore.CreateDeltaPrepareGenerations(ctx, []*model.DeltaPrepareGeneration{{
				PrepareID: prep.ID, OrgID: key.OrgID, ImageRepository: key.ImageRepository,
				SourceDigest: key.SourceDigest, TargetDigest: key.TargetDigest,
			}})
			Expect(err).ToNot(HaveOccurred())

			status := workerservice.NewStorePreparingStatus(fleets, devices)
			Expect(status.Set(ctx, orgId, domain.FleetKind, fleetName, 0, 1)).To(Succeed())
			completion, err := deltaprepare.NewCompletionService(deltaPrepareStore, status, eventsSvc)
			Expect(err).ToNot(HaveOccurred())
			generationSvc := deltageneration.NewServiceHandler(deltaGenerationStore, log)

			gen, err := deltaGenerationStore.GetDeltaGeneration(ctx, key)
			Expect(err).ToNot(HaveOccurred())
			gen.Status = model.DeltaGenerationSucceeded
			_, err = generationSvc.UpdateDeltaGeneration(ctx, gen.ResourceVersion, gen)
			Expect(err).ToNot(HaveOccurred())
			completeEvent, err := deltageneration.NewGenerationCompleteEvent(gen)
			Expect(err).ToNot(HaveOccurred())
			completionTask, err := generationcomplete.NewHandler(completion, status)
			Expect(err).ToNot(HaveOccurred())
			Expect(completionTask.Handle(ctx, worker_client.EventWithOrgId{OrgId: orgId, Event: *completeEvent})).To(Succeed())

			got, err := deltaPrepareStore.GetDeltaPrepare(ctx, deltapreparestore.PrepareKey{ID: prep.ID})
			Expect(err).ToNot(HaveOccurred())
			Expect(got.Status).To(Equal(model.DeltaPrepareComplete))

			listed, err := eventstore.NewEventStore(db, log).List(ctx, orgId, store.ListParams{Limit: 100})
			Expect(err).ToNot(HaveOccurred())
			reasons := make([]domain.EventReason, 0, len(listed.Items))
			for i := range listed.Items {
				reasons = append(reasons, listed.Items[i].Reason)
			}
			Expect(reasons).To(ContainElement(domain.EventReasonFleetRolloutStarted))

			fleet, err := fleets.Get(ctx, orgId, fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Metadata.Annotations).ToNot(BeNil())
			Expect((*fleet.Metadata.Annotations)[domain.FleetAnnotationTemplateVersion]).To(Equal(tvName))
		})
	})
})

type prepareEmitSpy struct {
	events []*domain.Event
}

func (s *prepareEmitSpy) emit(_ context.Context, _ uuid.UUID, event *domain.Event) error {
	if event == nil {
		return nil
	}
	s.events = append(s.events, event)
	return nil
}

func (s *prepareEmitSpy) reasons() []domain.EventReason {
	out := make([]domain.EventReason, 0, len(s.events))
	for _, ev := range s.events {
		out = append(out, ev.Reason)
	}
	return out
}

func fleetPrepareEvent(orgId uuid.UUID, fleet, tv string) worker_client.EventWithOrgId {
	details := domain.PrepareDeltasDetails{
		DetailType:      v1beta1.PrepareDeltas,
		TemplateVersion: lo.ToPtr(tv),
		ResourceVersion: lo.ToPtr("1"),
	}
	var eventDetails domain.EventDetails
	Expect(eventDetails.FromPrepareDeltasDetails(details)).To(Succeed())
	return worker_client.EventWithOrgId{
		OrgId: orgId,
		Event: domain.Event{
			Reason: domain.EventReasonPrepareDeltas,
			InvolvedObject: domain.ObjectReference{
				Kind: domain.FleetKind,
				Name: fleet,
			},
			Details: &eventDetails,
		},
	}
}
