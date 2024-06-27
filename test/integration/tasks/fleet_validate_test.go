package tasks_test

import (
	"context"
	"encoding/json"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/flightctl/flightctl/pkg/queues"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

var _ = Describe("FleetValidate", func() {
	var (
		log              *logrus.Logger
		ctx              context.Context
		orgId            uuid.UUID
		storeInst        store.Store
		cfg              *config.Config
		dbName           string
		callbackManager  tasks.CallbackManager
		fleet            *api.Fleet
		repository       *api.Repository
		goodGitConfig    *api.GitConfigProviderSpec
		badGitConfig     *api.GitConfigProviderSpec
		goodInlineConfig *api.InlineConfigProviderSpec
		badInlineConfig  *api.InlineConfigProviderSpec
		callback         store.FleetStoreCallback
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		storeInst, cfg, dbName = store.PrepareDBForUnitTests(log)
		ctrl := gomock.NewController(GinkgoT())
		publisher := queues.NewMockPublisher(ctrl)
		publisher.EXPECT().Publish(gomock.Any()).Return(nil).AnyTimes()
		callbackManager = tasks.NewCallbackManager(publisher, log)

		spec := api.RepositorySpec{}
		err := spec.FromGitGenericRepoSpec(api.GitGenericRepoSpec{
			Repo: "repo-url",
		})
		Expect(err).ToNot(HaveOccurred())
		repository = &api.Repository{
			Metadata: api.ObjectMeta{
				Name: util.StrToPtr("repo"),
			},
			Spec: spec,
		}
		repoCallback := store.RepositoryStoreCallback(func(*model.Repository) {})
		_, err = storeInst.Repository().Create(ctx, orgId, repository, repoCallback)
		Expect(err).ToNot(HaveOccurred())

		fleet = &api.Fleet{
			Metadata: api.ObjectMeta{
				Name: util.StrToPtr("myfleet"),
			},
			Spec: api.FleetSpec{},
		}

		goodGitConfig = &api.GitConfigProviderSpec{
			ConfigType: string(api.TemplateDiscriminatorGitConfig),
			Name:       "goodGitConfig",
		}
		goodGitConfig.GitRef.Path = "path"
		goodGitConfig.GitRef.Repository = "repo"
		goodGitConfig.GitRef.TargetRevision = "rev"

		badGitConfig = &api.GitConfigProviderSpec{
			ConfigType: string(api.TemplateDiscriminatorGitConfig),
			Name:       "badGitConfig",
		}
		badGitConfig.GitRef.Path = "path"
		badGitConfig.GitRef.Repository = "missingrepo"
		badGitConfig.GitRef.TargetRevision = "rev"

		goodInlineConfig = &api.InlineConfigProviderSpec{
			ConfigType: string(api.TemplateDiscriminatorInlineConfig),
			Name:       "goodInlineConfig",
		}
		var goodInline map[string]interface{}
		err = json.Unmarshal([]byte("{\"ignition\": {\"version\": \"3.4.0\"}}"), &goodInline)
		Expect(err).ToNot(HaveOccurred())
		goodInlineConfig.Inline = goodInline

		badInlineConfig = &api.InlineConfigProviderSpec{
			ConfigType: string(api.TemplateDiscriminatorInlineConfig),
			Name:       "badInlineConfig",
		}
		var badInline map[string]interface{}
		err = json.Unmarshal([]byte("{\"ignition\": {\"version\": \"badstring\"}}"), &badInline)
		Expect(err).ToNot(HaveOccurred())
		badInlineConfig.Inline = badInline

		callback = store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {})
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	When("a Fleet has a valid configuration", func() {
		It("creates a new TemplateVersion", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "myfleet", Kind: model.FleetKind}
			logic := tasks.NewFleetValidateLogic(callbackManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpec_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*goodGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.DeviceSpec_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpec_Config_Item{gitItem, inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
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
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.FleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusTrue))

			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*((repos.Items[0]).Metadata.Name)).To(Equal("repo"))
		})
	})

	When("a Fleet has an invalid git configuration", func() {
		It("sets an error Condition", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "myfleet", Kind: model.FleetKind}
			logic := tasks.NewFleetValidateLogic(callbackManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpec_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*badGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.DeviceSpec_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpec_Config_Item{gitItem, inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
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
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.FleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))

			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*((repos.Items[0]).Metadata.Name)).To(Equal("missingrepo"))
		})
	})

	When("a Fleet has an invalid inline configuration", func() {
		It("sets an error Condition", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "myfleet", Kind: model.FleetKind}
			logic := tasks.NewFleetValidateLogic(callbackManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpec_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*goodGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.DeviceSpec_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*badInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpec_Config_Item{gitItem, inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
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
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.FleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))

			repos, err := storeInst.Fleet().GetRepositoryRefs(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(repos.Items).To(HaveLen(1))
			Expect(*((repos.Items[0]).Metadata.Name)).To(Equal("repo"))
		})
	})

	When("a Fleet has an invalid configuration type", func() {
		It("sets an error Condition", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "myfleet", Kind: model.FleetKind}
			logic := tasks.NewFleetValidateLogic(callbackManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpec_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*goodGitConfig)
			Expect(err).ToNot(HaveOccurred())
			b, err := gitItem.MarshalJSON()
			Expect(err).ToNot(HaveOccurred())
			invalidStr := strings.ReplaceAll(string(b), "GitConfigProviderSpec", "InvalidProviderSpec")
			err = gitItem.UnmarshalJSON([]byte(invalidStr))
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpec_Config_Item{gitItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
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
			Expect(fleet.Status.Conditions[0].Type).To(Equal(api.FleetValid))
			Expect(fleet.Status.Conditions[0].Status).To(Equal(api.ConditionStatusFalse))
			Expect(fleet.Status.Conditions[0].Message).To(Equal("1 invalid configuration: <unknown>. Error: failed to find configuration item name: unsupported discriminator: InvalidProviderSpec"))
		})
	})
})
