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
	deltastore "github.com/flightctl/flightctl/internal/delta_worker/store"
	preparetask "github.com/flightctl/flightctl/internal/delta_worker/tasks/prepare"
	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	templateversionstore "github.com/flightctl/flightctl/internal/store/templateversion"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/flightctl/flightctl/test/util/testdb"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"gorm.io/gorm"
)

var _ = Describe("PrepareDeltas persist", func() {
	var (
		log              *logrus.Logger
		ctx              context.Context
		orgId            uuid.UUID
		cfg              *config.Config
		dbName           string
		db               *gorm.DB
		deltaStore       *deltastore.DeltaStore
		fleets           fleetstore.Store
		devices          devicestore.Store
		repos            repositorystore.Store
		templateVersions templateversionstore.Store
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		deltaStore = deltastore.NewStore(db, log.WithField("pkg", "delta-store"))
		Expect(deltaStore.InitialMigration(ctx)).To(Succeed())
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
			generationService := deltageneration.NewServiceHandler(deltaStore, deltaStore, deltaStore, nil, status, log)
			prepareService := deltaprepare.NewServiceHandler(deltaStore, status)
			prepareGenerationService := deltapreparegeneration.NewServiceHandler(deltaStore, generationService, prepareService, nil)
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

			waiting, err := deltaStore.GetDeltaPrepare(ctx, deltastore.PrepareKey{OrgID: orgId, Kind: domain.FleetKind, Name: fleetName}, deltastore.WithPrepareStatus(model.DeltaPrepareWaiting))
			Expect(err).ToNot(HaveOccurred())
			Expect(waiting).ToNot(BeNil())
			Expect(waiting.Status).To(Equal(model.DeltaPrepareWaiting))

			gen, err := deltaStore.GetDeltaGeneration(ctx, deltastore.GenerationKey{
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
		ResourceVersion: "1",
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
