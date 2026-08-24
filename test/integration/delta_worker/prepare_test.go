package delta_worker_test

import (
	"context"
	"encoding/json"
	"time"

	v1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	deltaworker "github.com/flightctl/flightctl/internal/delta_worker"
	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
	deltastore "github.com/flightctl/flightctl/internal/store/delta"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	"github.com/flightctl/flightctl/internal/store/model"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	"github.com/flightctl/flightctl/internal/store/selector"
	tvstore "github.com/flightctl/flightctl/internal/store/templateversion"
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
		log        *logrus.Logger
		ctx        context.Context
		orgId      uuid.UUID
		cfg        *config.Config
		dbName     string
		db         *gorm.DB
		deltaStore deltastore.Store
		fleets     fleetstore.Store
		devices    devicestore.Store
		repos      repositorystore.Store
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		deltaStore = deltastore.NewStore(db, log.WithField("pkg", "delta-store"))
		fleets = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		devices = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		repos = repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
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

			owner := util.SetResourceOwner(domain.FleetKind, fleetName)
			testutil.CreateTestDevices(ctx, 2, devices, orgId, owner, true)
			for _, name := range []string{"mydevice-1", "mydevice-2"} {
				device, err := devices.Get(ctx, orgId, name)
				Expect(err).ToNot(HaveOccurred())
				Expect(device.Status).ToNot(BeNil())
				device.Status.Os.ImageDigest = srcDigest
				device.Status.Capabilities = &domain.DeviceCapabilities{DeltaEligible: lo.ToPtr(true)}
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
			}, nil)
			Expect(err).ToNot(HaveOccurred())

			emit := &prepareEmitSpy{}
			p := &deltaworker.Preparer{
				Resolver: &deltaworker.Resolver{
					Fleet: func(ctx context.Context, org uuid.UUID, name string) (*domain.Fleet, error) {
						return fleets.Get(ctx, org, name)
					},
					TemplateVersion: func(_ context.Context, _ uuid.UUID, _, name string) (*domain.TemplateVersion, error) {
						return &domain.TemplateVersion{Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}}, nil
					},
					Devices: func(ctx context.Context, org uuid.UUID, owner string) ([]*domain.Device, error) {
						return listDevicesByOwner(ctx, devices, org, owner)
					},
					WriteTarget: func(ctx context.Context, org uuid.UUID) (*domain.OciRepoSpec, error) {
						repo, err := repos.GetDeltaStorageTarget(ctx, org)
						Expect(err).ToNot(HaveOccurred())
						Expect(repo).ToNot(BeNil())
						ociSpec, err := repo.Spec.AsOciRepoSpec()
						Expect(err).ToNot(HaveOccurred())
						return &ociSpec, nil
					},
					DesiredSpec: func(_ *domain.Device, _ *domain.TemplateVersion) (*domain.DeviceSpec, error) {
						return &domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: newImage}}, nil
					},
					Render: func(_ context.Context, spec *domain.DeviceSpec) (tasks.RenderedSpec, error) {
						return tasks.RenderedSpec{OsImage: spec.Os.Image}, nil
					},
					Inspect: func(_ context.Context, image string) (string, error) {
						Expect(image).To(Equal(newImage))
						return tgtDigest, nil
					},
				},
				Store:      deltaStore,
				Emit:       emit.emit,
				Now:        time.Now,
				MaxWait:    func(*domain.Fleet) *time.Duration { return nil },
				JobTimeout: func(*domain.Fleet) time.Duration { return 30 * time.Minute },
				Status:     deltaworker.NewStorePreparingStatus(fleets, devices),
				Resume:     func(context.Context, worker_client.EventWithOrgId) error { return nil },
			}

			Expect(p.Prepare(ctx, fleetPrepareEvent(orgId, fleetName, tvName))).To(Succeed())

			waiting, err := deltaStore.GetWaitingPrepare(ctx, orgId, domain.FleetKind, fleetName)
			Expect(err).ToNot(HaveOccurred())
			Expect(waiting).ToNot(BeNil())
			Expect(waiting.Status).To(Equal(model.DeltaPrepareWaiting))

			gen, err := deltaStore.GetGeneration(ctx, deltastore.GenerationKey{
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
			tvs := tvstore.NewTemplateVersionStore(db, log.WithField("pkg", "tv-store"))
			Expect(testutil.CreateTestTemplateVersion(ctx, tvs, orgId, fleetName, tvName, &domain.TemplateVersionStatus{
				Os: &domain.DeviceOsSpec{Image: "quay.io/acme/os:v2"},
			})).To(Succeed())

			ctrl := gomock.NewController(GinkgoT())
			DeferCleanup(ctrl.Finish)
			producer := queues.NewMockQueueProducer(ctrl)
			producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
			eventsSvc := events.NewServiceHandler(eventstore.NewEventStore(db, log.WithField("pkg", "event-store")), worker_client.NewWorkerClient(producer, log), log)
			fleetSvc := fleetservice.NewServiceHandler(fleets, nil, eventsSvc, log)
			deviceSvc := deviceservice.NewDeviceServiceHandler(devices, nil, fleets, eventsSvc, nil, "", log)
			tvSvc := templateversionservice.NewServiceHandler(tvs, nil, eventsSvc, log)

			key := deltastore.GenerationKey{
				OrgID:           orgId,
				ImageRepository: repoName,
				SourceDigest:    srcDigest,
				TargetDigest:    tgtDigest,
			}
			_, err := deltaStore.InsertGenerations(ctx, []*model.DeltaGeneration{{
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
			Expect(deltaStore.InsertPrepare(ctx, prep)).To(Succeed())
			Expect(deltaStore.InsertPrepareGenerations(ctx, prep.ID, []deltastore.GenerationKey{key})).To(Succeed())

			gen, err := deltaStore.GetGeneration(ctx, key)
			Expect(err).ToNot(HaveOccurred())
			Expect(deltaStore.CASGeneration(ctx, key, gen.ResourceVersion, deltastore.GenerationCAS{
				Status: model.DeltaGenerationSucceeded,
			})).To(Succeed())

			p := &deltaworker.Preparer{
				Store:     deltaStore,
				Events:    eventsSvc,
				FleetSvc:  fleetSvc,
				DeviceSvc: deviceSvc,
				TVSvc:     tvSvc,
				Status:    deltaworker.NewStorePreparingStatus(fleets, devices),
			}
			Expect(p.CompleteWaitingIfTerminal(ctx, key)).To(Succeed())

			got, err := deltaStore.GetPrepare(ctx, prep.ID)
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

func listDevicesByOwner(ctx context.Context, devices devicestore.Store, orgId uuid.UUID, owner string) ([]*domain.Device, error) {
	fs, err := selector.NewFieldSelectorFromMap(map[string]string{"metadata.owner": owner})
	if err != nil {
		return nil, err
	}
	list, err := devices.List(ctx, orgId, devicestore.DeviceListParams{ListParams: store.ListParams{FieldSelector: fs}})
	if err != nil {
		return nil, err
	}
	out := make([]*domain.Device, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}

func fleetPrepareEvent(orgId uuid.UUID, fleet, tv string) worker_client.EventWithOrgId {
	details := domain.PrepareDeltasDetails{
		DetailType:      v1beta1.PrepareDeltas,
		TemplateVersion: lo.ToPtr(tv),
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
