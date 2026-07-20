package tasks_test

import (
	"context"
	"slices"
	"strings"

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

var _ = Describe("FleetValidate", func() {
	var (
		log                  *logrus.Logger
		ctx                  context.Context
		orgId                uuid.UUID
		fleetStore           fleetstore.Store
		templateVersionSvc   templateversionservice.Service
		deviceSvc            deviceservice.Service
		repositorySvc        repositoryservice.Service
		fleetSvc             fleetservice.Service
		repositoryStore      repositorystore.Store
		templateVersionStore templateversionstore.Store
		cfg                  *config.Config
		dbName               string
		db                   *gorm.DB
		workerClient         worker_client.WorkerClient
		fleet                *api.Fleet
		repository           *api.Repository
		goodGitConfig        *api.GitConfigProviderSpec
		badGitConfig         *api.GitConfigProviderSpec
		goodInlineConfig     *api.InlineConfigProviderSpec
		badInlineConfig      *api.InlineConfigProviderSpec
		goodHttpConfig       *api.HttpConfigProviderSpec
		badHttpConfig        *api.HttpConfigProviderSpec
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		newFleetStore := fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		templateVersionStore = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		deviceStore := devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		repositoryStore = repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store"))
		eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		ctrl := gomock.NewController(GinkgoT())
		producer := queues.NewMockQueueProducer(ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		workerClient = worker_client.NewWorkerClient(producer, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())
		eventsSvc := events.NewServiceHandler(eventStore, workerClient, log)
		fleetSvc = fleetservice.NewServiceHandler(newFleetStore, eventsSvc, log)
		templateVersionSvc = templateversionservice.NewServiceHandler(templateVersionStore, kvStore, eventsSvc, log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(deviceStore, newFleetStore, eventsSvc, kvStore, "", log)
		repositorySvc = repositoryservice.NewServiceHandler(repositoryStore, eventsSvc, log)

		spec := api.RepositorySpec{}
		err = spec.FromGitRepoSpec(api.GitRepoSpec{
			Url:  "repo-url",
			Type: api.GitRepoSpecTypeGit,
		})
		Expect(err).ToNot(HaveOccurred())
		repository = &api.Repository{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("git-repo"),
			},
			Spec: spec,
		}
		specHttp := api.RepositorySpec{}
		err = specHttp.FromHttpRepoSpec(api.HttpRepoSpec{
			Url:  "http-repo-url",
			Type: api.HttpRepoSpecTypeHttp,
		})
		Expect(err).ToNot(HaveOccurred())
		repositoryHttp := &api.Repository{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("http-repo"),
			},
			Spec: specHttp,
		}

		repoCallback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
		_, err = repositoryStore.Create(ctx, orgId, repository, repoCallback)
		Expect(err).ToNot(HaveOccurred())
		_, err = repositoryStore.Create(ctx, orgId, repositoryHttp, repoCallback)
		Expect(err).ToNot(HaveOccurred())

		fleet = &api.Fleet{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("myfleet"),
			},
		}

		goodGitConfig = &api.GitConfigProviderSpec{
			Name: "goodGitConfig",
		}
		goodGitConfig.GitRef.Path = "path-{{ device.metadata.name }}"
		goodGitConfig.GitRef.Repository = "git-repo"
		goodGitConfig.GitRef.TargetRevision = "rev"

		badGitConfig = &api.GitConfigProviderSpec{
			Name: "badGitConfig",
		}
		badGitConfig.GitRef.Path = "path"
		badGitConfig.GitRef.Repository = "missingrepo"
		badGitConfig.GitRef.TargetRevision = "rev"

		goodInlineConfig = &api.InlineConfigProviderSpec{
			Name: "goodInlineConfig",
		}
		base64 := api.EncodingBase64
		goodInlineConfig.Inline = []api.FileSpec{
			{Path: "/etc/base64encoded", Content: "SGVsbG8gd29ybGQsIHdoYXQncyB1cD8=", ContentEncoding: &base64},
			{Path: "/etc/notencoded", Content: "Hello world, what's up?"},
		}

		badInlineConfig = &api.InlineConfigProviderSpec{
			Name: "badInlineConfig",
		}
		badInlineConfig.Inline = []api.FileSpec{
			{Path: "/etc/base64encoded", Content: "SGVsbG8gd29ybGQsIHdoYXQncyB1cD8=", ContentEncoding: &base64},
			{Path: "/etc/notencoded", Content: "Hello world, what's up?", ContentEncoding: &base64},
		}

		goodHttpConfig = &api.HttpConfigProviderSpec{
			Name: "goodHttpConfig",
		}
		goodHttpConfig.HttpRef.Repository = "http-repo"
		goodHttpConfig.HttpRef.FilePath = "http-path-{{ device.metadata.labels[key] }}"
		goodHttpConfig.HttpRef.Suffix = lo.ToPtr("/suffix")

		badHttpConfig = &api.HttpConfigProviderSpec{
			Name: "badHttpConfig",
		}
		badHttpConfig.HttpRef.Repository = "http-missingrepo"
		badHttpConfig.HttpRef.FilePath = "http-path"
		badHttpConfig.HttpRef.Suffix = lo.ToPtr("/suffix")
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	When("a Fleet has a valid configuration", func() {
		It("creates a new TemplateVersion", func() {
			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.FleetKind,
					Name: "myfleet",
				},
			}
			logic := tasks.NewFleetValidateLogic(log, fleetSvc, templateVersionSvc, deviceSvc, repositorySvc, nil, orgId, event)

			gitItem := api.ConfigProviderSpec{}
			err := gitItem.FromGitConfigProviderSpec(*goodGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.ConfigProviderSpec{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			httpItem := api.ConfigProviderSpec{}
			err = httpItem.FromHttpConfigProviderSpec(*goodHttpConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.ConfigProviderSpec{gitItem, inlineItem, httpItem}

			tvList, err := templateVersionStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = fleetStore.Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx)
			Expect(err).ToNot(HaveOccurred())

			tvList, err = templateVersionStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(1))

			fleet, err = fleetStore.Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(fleet.Status.Conditions).To(HaveLen(1))
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusTrue))

			repos, err := fleetStore.GetRepositoryRefs(ctx, orgId, "myfleet")

			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(2))
			repoNames := []string{*((repos.Items[0]).Metadata.Name), *((repos.Items[1]).Metadata.Name)}
			slices.Sort(repoNames)
			Expect(repoNames[0]).To(Equal("git-repo"))
			Expect(repoNames[1]).To(Equal("http-repo"))

		})
	})

	When("a Fleet has an invalid git configuration", func() {
		It("sets an error Condition", func() {
			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.FleetKind,
					Name: "myfleet",
				},
			}
			logic := tasks.NewFleetValidateLogic(log, fleetSvc, templateVersionSvc, deviceSvc, repositorySvc, nil, orgId, event)

			gitItem := api.ConfigProviderSpec{}
			err := gitItem.FromGitConfigProviderSpec(*badGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.ConfigProviderSpec{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			httpItem := api.ConfigProviderSpec{}
			err = httpItem.FromHttpConfigProviderSpec(*goodHttpConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.ConfigProviderSpec{gitItem, inlineItem, httpItem}

			tvList, err := templateVersionStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = fleetStore.Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx)
			Expect(err).To(HaveOccurred())

			tvList, err = templateVersionStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = fleetStore.Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(fleet.Status.Conditions).To(HaveLen(1))
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))

			repos, err := fleetStore.GetRepositoryRefs(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(2))
			repoNames := []string{*((repos.Items[0]).Metadata.Name), *((repos.Items[1]).Metadata.Name)}
			slices.Sort(repoNames)
			Expect(repoNames[0]).To(Equal("http-repo"))
			Expect(repoNames[1]).To(Equal("missingrepo"))
		})
	})

	When("a Fleet has an invalid http configuration", func() {
		It("sets an error Condition", func() {
			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.FleetKind,
					Name: "myfleet",
				},
			}
			logic := tasks.NewFleetValidateLogic(log, fleetSvc, templateVersionSvc, deviceSvc, repositorySvc, nil, orgId, event)

			gitItem := api.ConfigProviderSpec{}
			err := gitItem.FromGitConfigProviderSpec(*goodGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.ConfigProviderSpec{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			httpItem := api.ConfigProviderSpec{}
			err = httpItem.FromHttpConfigProviderSpec(*badHttpConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.ConfigProviderSpec{gitItem, inlineItem, httpItem}

			tvList, err := templateVersionStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = fleetStore.Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx)
			Expect(err).To(HaveOccurred())

			tvList, err = templateVersionStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = fleetStore.Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(fleet.Status.Conditions).To(HaveLen(1))
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))

			repos, err := fleetStore.GetRepositoryRefs(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(2))
			repoNames := []string{*((repos.Items[0]).Metadata.Name), *((repos.Items[1]).Metadata.Name)}
			slices.Sort(repoNames)
			Expect(repoNames[0]).To(Equal("git-repo"))
			Expect(repoNames[1]).To(Equal("http-missingrepo"))
		})
	})

	When("a Fleet has an invalid configuration type", func() {
		It("sets an error Condition", func() {
			event := api.Event{
				Reason: api.EventReasonResourceUpdated,
				InvolvedObject: api.ObjectReference{
					Kind: api.FleetKind,
					Name: "myfleet",
				},
			}
			logic := tasks.NewFleetValidateLogic(log, fleetSvc, templateVersionSvc, deviceSvc, repositorySvc, nil, orgId, event)

			gitItem := api.ConfigProviderSpec{}
			err := gitItem.FromGitConfigProviderSpec(*goodGitConfig)
			Expect(err).ToNot(HaveOccurred())
			b, err := gitItem.MarshalJSON()
			Expect(err).ToNot(HaveOccurred())
			invalidStr := strings.ReplaceAll(string(b), "gitRef", "inline")
			err = gitItem.UnmarshalJSON([]byte(invalidStr))
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.ConfigProviderSpec{gitItem}

			tvList, err := templateVersionStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = fleetStore.Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx)
			Expect(err).To(HaveOccurred())

			tvList, err = templateVersionStore.List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = fleetStore.Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(fleet.Status.Conditions).To(HaveLen(1))
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))
			Expect(fleet.Status.Conditions[0].Message).To(ContainSubstring("failed getting config item as InlineConfigProviderSpec"))
		})
	})
})
