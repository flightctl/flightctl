package tasks_test

import (
	"context"
	"slices"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/worker_client"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

var _ = Describe("FleetValidate", func() {
	var (
		log              *logrus.Logger
		ctx              context.Context
		orgId            uuid.UUID
		storeInst        store.Store
		serviceHandler   service.Service
		cfg              *config.Config
		dbName           string
		workerClient     worker_client.WorkerClient
		fleet            *api.Fleet
		repository       *api.Repository
		goodGitConfig    *api.GitConfigProviderSpec
		badGitConfig     *api.GitConfigProviderSpec
		goodInlineConfig *api.InlineConfigProviderSpec
		badInlineConfig  *api.InlineConfigProviderSpec
		goodHttpConfig   *api.HttpConfigProviderSpec
		badHttpConfig    *api.HttpConfigProviderSpec
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		ctx = context.WithValue(ctx, consts.InternalRequestCtxKey, true)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(ctx, log)
		ctrl := gomock.NewController(GinkgoT())
		producer := queues.NewMockQueueProducer(ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil).AnyTimes()
		workerClient = worker_client.NewWorkerClient(producer, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, workerClient, kvStore, nil, log, "", "", []string{})

		spec := api.RepositorySpec{}
		err = spec.FromGenericRepoSpec(api.GenericRepoSpec{
			Url:  "repo-url",
			Type: "git",
		})
		Expect(err).ToNot(HaveOccurred())
		repository = &api.Repository{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("git-repo"),
			},
			Spec: spec,
		}
		specHttp := api.RepositorySpec{}
		err = specHttp.FromGenericRepoSpec(api.GenericRepoSpec{
			Url:  "http-repo-url",
			Type: "http",
		})
		Expect(err).ToNot(HaveOccurred())
		repositoryHttp := &api.Repository{
			Metadata: api.ObjectMeta{
				Name: lo.ToPtr("http-repo"),
			},
			Spec: specHttp,
		}

		repoCallback := store.EventCallback(func(context.Context, api.ResourceKind, uuid.UUID, string, interface{}, interface{}, bool, error) {})
		_, err = storeInst.Repository().Create(ctx, orgId, repository, repoCallback)
		Expect(err).ToNot(HaveOccurred())
		_, err = storeInst.Repository().Create(ctx, orgId, repositoryHttp, repoCallback)
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
		store.DeleteTestDB(ctx, log, cfg, storeInst, dbName)
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
			logic := tasks.NewFleetValidateLogic(log, serviceHandler, nil, orgId, event)

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

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx)
			Expect(err).ToNot(HaveOccurred())

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(1))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(fleet.Status.Conditions).To(HaveLen(1))
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusTrue))

			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet")

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
			logic := tasks.NewFleetValidateLogic(log, serviceHandler, nil, orgId, event)

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

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx)
			Expect(err).To(HaveOccurred())

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(fleet.Status.Conditions).To(HaveLen(1))
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))

			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet")
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
			logic := tasks.NewFleetValidateLogic(log, serviceHandler, nil, orgId, event)

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

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx)
			Expect(err).To(HaveOccurred())

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(fleet.Status.Conditions).To(HaveLen(1))
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))

			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet")
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
			logic := tasks.NewFleetValidateLogic(log, serviceHandler, nil, orgId, event)

			gitItem := api.ConfigProviderSpec{}
			err := gitItem.FromGitConfigProviderSpec(*goodGitConfig)
			Expect(err).ToNot(HaveOccurred())
			b, err := gitItem.MarshalJSON()
			Expect(err).ToNot(HaveOccurred())
			invalidStr := strings.ReplaceAll(string(b), "gitRef", "inline")
			err = gitItem.UnmarshalJSON([]byte(invalidStr))
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.ConfigProviderSpec{gitItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, nil)
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx)
			Expect(err).To(HaveOccurred())

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(fleet.Status.Conditions).To(HaveLen(1))
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.ConditionTypeFleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))
			Expect(fleet.Status.Conditions[0].Message).To(ContainSubstring("failed getting config item as InlineConfigProviderSpec"))
		})
	})
})
