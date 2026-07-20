package tasks_test

import (
	"context"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	repositorystore "github.com/flightctl/flightctl/internal/store/repository"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
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

var _ = Describe("RepoUpdate", func() {
	var (
		log           *logrus.Logger
		ctx           context.Context
		orgId         uuid.UUID
		fleetStore    fleetstore.Store
		deviceStore   devicestore.Store
		repositorySvc repositoryservice.Service
		eventSvc      eventservice.Service
		cfg           *config.Config
		dbName        string
		db            *gorm.DB
		workerClient  *worker_client.MockWorkerClient
		ctrl          *gomock.Controller
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		repositoryStore := repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		ctrl = gomock.NewController(GinkgoT())
		workerClient = worker_client.NewMockWorkerClient(ctrl)
		eventsSvc := events.NewServiceHandler(eventStore, workerClient, log)
		repositorySvc = repositoryservice.NewServiceHandler(repositoryStore, eventsSvc, log)
		eventSvc = eventservice.NewServiceHandler(eventStore, eventsSvc)

		// Create 2 git config items, each to a different repo
		err = testutil.CreateRepositories(ctx, 2, repositoryStore, orgId)
		Expect(err).ToNot(HaveOccurred())

		gitConfig1 := &api.GitConfigProviderSpec{
			Name: "gitConfig1",
		}
		gitConfig1.GitRef.Path = "path"
		gitConfig1.GitRef.Repository = "myrepository-1"
		gitConfig1.GitRef.TargetRevision = "rev"
		gitItem1 := api.ConfigProviderSpec{}
		err = gitItem1.FromGitConfigProviderSpec(*gitConfig1)
		Expect(err).ToNot(HaveOccurred())

		gitConfig2 := &api.GitConfigProviderSpec{
			Name: "gitConfig2",
		}
		gitConfig2.GitRef.Path = "path"
		gitConfig2.GitRef.Repository = "myrepository-2"
		gitConfig2.GitRef.TargetRevision = "rev"
		gitItem2 := api.ConfigProviderSpec{}
		err = gitItem2.FromGitConfigProviderSpec(*gitConfig2)
		Expect(err).ToNot(HaveOccurred())

		// Create an inline config item
		inlineConfig := &api.InlineConfigProviderSpec{
			Name: "inlineConfig",
		}
		base64 := api.EncodingBase64
		inlineConfig.Inline = []api.FileSpec{
			{Path: "/etc/base64encoded", Content: "SGVsbG8gd29ybGQsIHdoYXQncyB1cD8=", ContentEncoding: &base64},
			{Path: "/etc/notencoded", Content: "Hello world, what's up?"},
		}
		inlineItem := api.ConfigProviderSpec{}
		err = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)
		Expect(err).ToNot(HaveOccurred())

		config1 := []api.ConfigProviderSpec{gitItem1, inlineItem}
		config2 := []api.ConfigProviderSpec{gitItem2, inlineItem}

		// Create fleet1 referencing repo1, fleet2 referencing repo2
		fleet1 := api.Fleet{
			Metadata: api.ObjectMeta{Name: lo.ToPtr("fleet1")},
			Spec:     api.FleetSpec{},
		}
		fleet1.Spec.Template.Spec = api.DeviceSpec{Config: &config1}

		fleet2 := api.Fleet{
			Metadata: api.ObjectMeta{Name: lo.ToPtr("fleet2")},
		}
		fleet2.Spec.Template.Spec = api.DeviceSpec{Config: &config2}

		_, err = fleetStore.Create(ctx, orgId, &fleet1, nil)
		Expect(err).ToNot(HaveOccurred())
		err = fleetStore.OverwriteRepositoryRefs(ctx, orgId, "fleet1", "myrepository-1")
		Expect(err).ToNot(HaveOccurred())
		_, err = fleetStore.Create(ctx, orgId, &fleet2, nil)
		Expect(err).ToNot(HaveOccurred())
		err = fleetStore.OverwriteRepositoryRefs(ctx, orgId, "fleet2", "myrepository-2")
		Expect(err).ToNot(HaveOccurred())

		// Create device1 referencing repo1, device2 referencing repo2
		device1 := api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr("device1")},
			Spec: &api.DeviceSpec{
				Config: &config1,
			},
		}

		device2 := api.Device{
			Metadata: api.ObjectMeta{Name: lo.ToPtr("device2")},
			Spec: &api.DeviceSpec{
				Config: &config2,
			},
		}

		_, err = deviceStore.Create(ctx, orgId, &device1, nil)
		Expect(err).ToNot(HaveOccurred())
		err = deviceStore.OverwriteRepositoryRefs(ctx, orgId, "device1", "myrepository-1")
		Expect(err).ToNot(HaveOccurred())
		_, err = deviceStore.Create(ctx, orgId, &device2, nil)
		Expect(err).ToNot(HaveOccurred())
		err = deviceStore.OverwriteRepositoryRefs(ctx, orgId, "device2", "myrepository-2")
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		ctrl.Finish()
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	When("a Repository definition is updated", func() {
		It("refreshes relevant fleets and devices", func() {
			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.RepositoryKind,
					Name: "myrepository-1",
				},
			}
			logic := tasks.NewRepositoryUpdateLogic(log, repositorySvc, eventSvc, orgId, event)
			workerClient.EXPECT().EmitEvent(gomock.Any(), gomock.Any(), gomock.Any()).Times(2)
			err := logic.HandleRepositoryUpdate(ctx)
			Expect(err).ToNot(HaveOccurred())

		})
	})
})
