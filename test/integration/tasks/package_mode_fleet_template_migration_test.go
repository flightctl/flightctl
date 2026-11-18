package tasks_test

import (
	"context"
	"time"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/domain"
	catalogservice "github.com/flightctl/flightctl/internal/service/catalog"
	checkpointservice "github.com/flightctl/flightctl/internal/service/checkpoint"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	"github.com/flightctl/flightctl/internal/store"
	catalogstore "github.com/flightctl/flightctl/internal/store/catalog"
	checkpointstore "github.com/flightctl/flightctl/internal/store/checkpoint"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
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

var _ = Describe("PackageModeFleetTemplateMigration", func() {
	var (
		log              *logrus.Logger
		ctx              context.Context
		orgId            uuid.UUID
		cfg              *config.Config
		db               *gorm.DB
		dbName           string
		fleetSvc         fleetservice.Service
		catalogSvc       catalogservice.Service
		checkpointSvc    checkpointservice.Service
		migrationTask    *tasks.PackageModeFleetTemplateMigrationTask
		ctrl             *gomock.Controller
		migrationCfg     tasks.PackageModeTemplateMigrationConfig
		createFleetNamed func(name, image string, owner *string)
		readFleet        func(name string) *domain.Fleet
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())

		fStore := fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		eStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		dStore := devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		cStore := catalogstore.NewCatalogStore(db, log.WithField("pkg", "catalog-store"))
		cpStore := checkpointstore.NewCheckpointStore(db, log.WithField("pkg", "checkpoint-store"))
		Expect(cpStore.InitialMigration(ctx)).To(Succeed())

		ctrl = gomock.NewController(GinkgoT())
		producer := queues.NewMockQueueProducer(ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		eventsSvc := events.NewServiceHandler(eStore, worker_client.NewWorkerClient(producer, log), log)

		// The catalog store is wired in so that catalog item references are
		// validated for real on every create and on the migration's own write.
		fleetSvc = fleetservice.NewServiceHandler(fStore, cStore, eventsSvc, log)
		catalogSvc = catalogservice.NewServiceHandler(cStore, dStore, fStore, eventsSvc, log)
		checkpointSvc = checkpointservice.NewServiceHandler(cpStore)
		migrationCfg = tasks.PackageModeTemplateMigrationConfig{
			PageSize:   50,
			BatchSize:  50,
			TimeBudget: time.Minute,
		}
		migrationTask, err = tasks.NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpointSvc, migrationCfg, log)
		Expect(err).NotTo(HaveOccurred())

		createFleetNamed = func(name, image string, owner *string) {
			fleet := domain.Fleet{
				ApiVersion: domain.FleetAPIVersion,
				Kind:       domain.FleetKind,
				Metadata:   domain.ObjectMeta{Name: lo.ToPtr(name), Owner: owner},
			}
			if image != "" {
				fleet.Spec.Template.Spec.Os = &domain.DeviceOsSpec{Image: image}
			}
			created, status := fleetSvc.CreateFleet(ctx, orgId, fleet)
			Expect(status.Code).To(BeEquivalentTo(201), "creating %q: %s", name, status.Message)
			Expect(created).NotTo(BeNil())
		}

		readFleet = func(name string) *domain.Fleet {
			fleet, status := fleetSvc.GetFleet(ctx, orgId, name, domain.GetFleetParams{})
			Expect(status.Code).To(BeEquivalentTo(200))
			return fleet
		}
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	Context("when fleets are discovered", func() {
		It("should guard an eligible image and leave the rest of the fleets alone", func() {
			ownedBySync := lo.ToPtr("ResourceSync/from-git")
			createFleetNamed("literal", "quay.io/org/os:v2", nil)
			createFleetNamed("parameterized", "quay.io/org/os:{{ .metadata.labels.version }}", nil)
			createFleetNamed("already-guarded", "{{ if deviceHasImageMode }}quay.io/org/os:v9{{ end }}", nil)
			createFleetNamed("unrelated-guard", "{{ if .metadata.labels.tier }}quay.io/org/os:v9{{ end }}", nil)
			createFleetNamed("no-image", "", nil)
			createFleetNamed("from-git", "quay.io/org/os:v2", ownedBySync)

			guardedBefore := readFleet("already-guarded")
			unrelatedBefore := readFleet("unrelated-guard")
			gitBefore := readFleet("from-git")

			migrationTask.Poll(ctx, orgId)

			// The eligible fleets are wrapped, byte for byte.
			Expect(readFleet("literal").Spec.Template.Spec.Os.Image).
				To(Equal("{{ if deviceHasImageMode }}quay.io/org/os:v2{{ end }}"))
			Expect(readFleet("parameterized").Spec.Template.Spec.Os.Image).
				To(Equal("{{ if deviceHasImageMode }}quay.io/org/os:{{ .metadata.labels.version }}{{ end }}"))

			// Everything else is untouched, proven by an unchanged resource version.
			Expect(lo.FromPtr(readFleet("already-guarded").Metadata.ResourceVersion)).
				To(Equal(lo.FromPtr(guardedBefore.Metadata.ResourceVersion)))
			Expect(lo.FromPtr(readFleet("unrelated-guard").Metadata.ResourceVersion)).
				To(Equal(lo.FromPtr(unrelatedBefore.Metadata.ResourceVersion)))
			Expect(lo.FromPtr(readFleet("from-git").Metadata.ResourceVersion)).
				To(Equal(lo.FromPtr(gitBefore.Metadata.ResourceVersion)))
			Expect(readFleet("no-image").Spec.Template.Spec.Os).To(BeNil())
		})

		It("should produce a fleet that the real admission path accepts", func() {
			createFleetNamed("literal", "quay.io/org/os:v2", nil)
			migrationTask.Poll(ctx, orgId)

			migrated := readFleet("literal")
			Expect(migrated.Spec.Template.Spec.Os.Image).
				To(Equal("{{ if deviceHasImageMode }}quay.io/org/os:v2{{ end }}"))

			// The write above already went through Fleet.Validate(); replaying the
			// stored spec proves the result is admissible on its own terms too.
			Expect(migrated.Validate()).To(BeEmpty())
			_, status := fleetSvc.ReplaceFleet(ctx, orgId, "literal", *migrated, false)
			Expect(status.Code).To(BeEquivalentTo(200), status.Message)
		})

		It("should make no writes on a second run", func() {
			createFleetNamed("literal", "quay.io/org/os:v2", nil)

			migrationTask.Poll(ctx, orgId)
			afterFirst := readFleet("literal")

			// A fresh task so the second run starts from the durable checkpoint
			// rather than any in-process state.
			secondTask, err := tasks.NewPackageModeFleetTemplateMigrationTask(fleetSvc, checkpointSvc, migrationCfg, log)
			Expect(err).NotTo(HaveOccurred())
			secondTask.Poll(ctx, orgId)

			afterSecond := readFleet("literal")
			Expect(lo.FromPtr(afterSecond.Metadata.ResourceVersion)).
				To(Equal(lo.FromPtr(afterFirst.Metadata.ResourceVersion)),
					"a completed migration must not write again")
		})

		It("should preserve a catalog item reference alongside an eligible image", func() {
			const (
				catalogName = "example-catalog"
				itemName    = "example-item"
				itemVersion = "1.0.0"
			)
			_, status := catalogSvc.CreateCatalog(ctx, orgId, domain.Catalog{
				Metadata: domain.ObjectMeta{Name: lo.ToPtr(catalogName)},
				Spec:     domain.CatalogSpec{DisplayName: lo.ToPtr("Example Catalog")},
			})
			Expect(status.Code).To(BeEquivalentTo(201), status.Message)

			_, status = catalogSvc.CreateCatalogItem(ctx, orgId, catalogName, domain.CatalogItem{
				Metadata: domain.CatalogItemMeta{Name: lo.ToPtr(itemName)},
				Spec: domain.CatalogItemSpec{
					DisplayName: lo.ToPtr("Example Application"),
					Category:    lo.ToPtr(domain.CatalogItemCategoryApplication),
					Type:        domain.CatalogItemTypeContainer,
					Artifacts: []domain.CatalogItemArtifact{
						{Type: domain.CatalogItemArtifactTypeContainer, Uri: "quay.io/org/app"},
					},
					Versions: []domain.CatalogItemVersion{{
						Version:    itemVersion,
						References: map[domain.CatalogItemArtifactType]string{domain.CatalogItemArtifactTypeContainer: "v1"},
						Channels:   []string{"stable"},
					}},
				},
			})
			Expect(status.Code).To(BeEquivalentTo(201), status.Message)

			// A catalog item reference rides on an application: the API rejects a
			// device OS that names both an image and a reference.
			ref := domain.CatalogItemRefSpec{Catalog: catalogName, Item: itemName, Version: itemVersion}
			container := domain.ContainerApplication{AppType: domain.AppTypeContainer, Name: lo.ToPtr("catalog-app")}
			Expect(container.FromCatalogItemRefApplicationProviderSpec(
				domain.CatalogItemRefApplicationProviderSpec{CatalogItemRef: ref})).To(Succeed())
			var app domain.ApplicationProviderSpec
			Expect(app.FromContainerApplication(container)).To(Succeed())

			fleet := domain.Fleet{
				ApiVersion: domain.FleetAPIVersion,
				Kind:       domain.FleetKind,
				Metadata:   domain.ObjectMeta{Name: lo.ToPtr("with-catalog")},
			}
			fleet.Spec.Template.Spec.Os = &domain.DeviceOsSpec{Image: "quay.io/org/os:v2"}
			fleet.Spec.Template.Spec.Applications = lo.ToPtr([]domain.ApplicationProviderSpec{app})
			created, status := fleetSvc.CreateFleet(ctx, orgId, fleet)
			Expect(status.Code).To(BeEquivalentTo(201), status.Message)
			Expect(created).NotTo(BeNil())

			migrationTask.Poll(ctx, orgId)

			// The image is guarded even though the fleet carries a reference, and
			// the reference itself comes back untouched.
			migrated := readFleet("with-catalog")
			Expect(migrated.Spec.Template.Spec.Os.Image).
				To(Equal("{{ if deviceHasImageMode }}quay.io/org/os:v2{{ end }}"))

			apps := lo.FromPtr(migrated.Spec.Template.Spec.Applications)
			Expect(apps).To(HaveLen(1))
			migratedContainer, err := apps[0].AsContainerApplication()
			Expect(err).NotTo(HaveOccurred())
			Expect(lo.FromPtr(migratedContainer.Name)).To(Equal("catalog-app"))
			migratedProvider, err := migratedContainer.AsCatalogItemRefApplicationProviderSpec()
			Expect(err).NotTo(HaveOccurred())
			Expect(migratedProvider.CatalogItemRef).To(Equal(ref))
			Expect(migrated.Validate()).To(BeEmpty())
		})
	})
})
