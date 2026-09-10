package periodic_test

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
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
	tvstore "github.com/flightctl/flightctl/internal/store/templateversion"
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
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

var _ = Describe("Delta prepare deadline poll", func() {
	var (
		ctx        context.Context
		orgId      uuid.UUID
		cfg        *config.Config
		dbName     string
		db         *gorm.DB
		deltaStore deltastore.Store
		fleets     fleetstore.Store
		devices    devicestore.Store
		tvs        tvstore.Store
		eventStore eventstore.Store
		deadline   *tasks.DeltaPrepareDeadline
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		log := flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		deltaStore = deltastore.NewStore(db, log.WithField("pkg", "delta-store"))
		fleets = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		devices = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		tvs = tvstore.NewTemplateVersionStore(db, log.WithField("pkg", "tv-store"))
		eventStore = eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		orgs := organizationstore.NewOrganizationStore(db)
		orgId = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, orgs, orgId)).To(Succeed())

		ctrl := gomock.NewController(GinkgoT())
		DeferCleanup(ctrl.Finish)
		producer := queues.NewMockQueueProducer(ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		eventsSvc := events.NewServiceHandler(eventStore, worker_client.NewWorkerClient(producer, log), log)
		deadline = tasks.NewDeltaPrepareDeadline(
			log,
			deltaStore,
			fleetservice.NewServiceHandler(fleets, nil, eventsSvc, log),
			deviceservice.NewDeviceServiceHandler(devices, nil, fleets, eventsSvc, nil, "", log),
			templateversionservice.NewServiceHandler(tvs, nil, eventsSvc, log),
			eventservice.NewServiceHandler(eventStore, eventsSvc),
		)
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, flightlog.InitLogs(), cfg, db, dbName)).To(Succeed())
	})

	eventReasons := func() []domain.EventReason {
		listed, err := eventStore.List(ctx, orgId, store.ListParams{Limit: 100})
		Expect(err).ToNot(HaveOccurred())
		out := make([]domain.EventReason, 0, len(listed.Items))
		for i := range listed.Items {
			out = append(out, listed.Items[i].Reason)
		}
		return out
	}

	It("should fail a past-deadline waiting prepare and emit FleetRolloutStarted without changing generation status", func() {
		const (
			fleetName = "expired-fleet"
			tvName    = "tv-expired"
			repoName  = "quay.io/acme/os"
		)
		testutil.CreateTestFleet(ctx, fleets, orgId, fleetName, nil, nil)
		Expect(testutil.CreateTestTemplateVersion(ctx, tvs, orgId, fleetName, tvName, nil)).To(Succeed())

		key := deltastore.GenerationKey{
			OrgID:           orgId,
			ImageRepository: repoName,
			SourceDigest:    "sha256:src",
			TargetDigest:    "sha256:tgt",
		}
		_, err := deltaStore.InsertGenerations(ctx, []*model.DeltaGeneration{{
			OrgID:           orgId,
			ImageRepository: key.ImageRepository,
			SourceDigest:    key.SourceDigest,
			TargetDigest:    key.TargetDigest,
			Status:          model.DeltaGenerationInProgress,
		}})
		Expect(err).ToNot(HaveOccurred())

		past := time.Now().Add(-time.Hour)
		tv := tvName
		prep := &model.DeltaPrepare{
			OrgID:           orgId,
			Kind:            domain.FleetKind,
			Name:            fleetName,
			TemplateVersion: &tv,
			Deadline:        &past,
		}
		Expect(deltaStore.InsertPrepare(ctx, prep)).To(Succeed())

		deadline.Poll(ctx)

		got, err := deltaStore.GetPrepare(ctx, prep.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.Status).To(Equal(model.DeltaPrepareFailed))

		gen, err := deltaStore.GetGeneration(ctx, key)
		Expect(err).ToNot(HaveOccurred())
		Expect(gen.Status).To(Equal(model.DeltaGenerationInProgress))

		Expect(eventReasons()).To(ContainElement(domain.EventReasonFleetRolloutStarted))
	})

	It("should leave a waiting prepare with a null deadline waiting", func() {
		const fleetName = "no-deadline"
		testutil.CreateTestFleet(ctx, fleets, orgId, fleetName, nil, nil)
		Expect(testutil.CreateTestTemplateVersion(ctx, tvs, orgId, fleetName, "tv-1", nil)).To(Succeed())
		prep := &model.DeltaPrepare{
			OrgID:           orgId,
			Kind:            domain.FleetKind,
			Name:            fleetName,
			TemplateVersion: lo.ToPtr("tv-1"),
		}
		Expect(deltaStore.InsertPrepare(ctx, prep)).To(Succeed())

		deadline.Poll(ctx)

		got, err := deltaStore.GetPrepare(ctx, prep.ID)
		Expect(err).ToNot(HaveOccurred())
		Expect(got.Status).To(Equal(model.DeltaPrepareWaiting))
		Expect(eventReasons()).NotTo(ContainElement(domain.EventReasonFleetRolloutStarted))
	})
})
