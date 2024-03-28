package tasks_test

import (
	"context"
	"encoding/json"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/model"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/util"
	flightlog "github.com/flightctl/flightctl/pkg/log"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("FleetValidate", func() {
	var (
		log              *logrus.Logger
		ctx              context.Context
		orgId            uuid.UUID
		storeInst        store.Store
		cfg              *config.Config
		dbName           string
		taskManager      tasks.TaskManager
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
		taskManager = tasks.Init(log, storeInst)

		repository = &api.Repository{
			Metadata: api.ObjectMeta{
				Name: util.StrToPtr("repo"),
			},
			Spec: api.RepositorySpec{
				Repo: util.StrToPtr("repo-url"),
			},
		}
		repoCallback := store.RepositoryStoreCallback(func(*model.Repository) {})
		_, err := storeInst.Repository().Create(ctx, orgId, repository, repoCallback)
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
			logic := tasks.NewFleetValidateLogic(taskManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpecification_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*goodGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.DeviceSpecification_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpecification_Config_Item{gitItem, inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx, fleet)
			Expect(err).ToNot(HaveOccurred())

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(1))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(*fleet.Status.Conditions).To(HaveLen(1))
			Expect((*fleet.Status.Conditions)[0].Type).To(Equal(api.FleetValid))
			Expect((*fleet.Status.Conditions)[0].Status).To(Equal(api.ConditionStatusTrue))
		})
	})

	When("a Fleet has an invalid git configuration", func() {
		It("sets an error Condition", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "myfleet", Kind: model.FleetKind}
			logic := tasks.NewFleetValidateLogic(taskManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpecification_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*badGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.DeviceSpecification_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpecification_Config_Item{gitItem, inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx, fleet)
			Expect(err).To(HaveOccurred())

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(*fleet.Status.Conditions).To(HaveLen(1))
			Expect((*fleet.Status.Conditions)[0].Type).To(Equal(api.FleetValid))
			Expect((*fleet.Status.Conditions)[0].Status).To(Equal(api.ConditionStatusFalse))
		})
	})

	When("a Fleet has an invalid inline configuration", func() {
		It("sets an error Condition", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "myfleet", Kind: model.FleetKind}
			logic := tasks.NewFleetValidateLogic(taskManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpecification_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*goodGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.DeviceSpecification_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*badInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpecification_Config_Item{gitItem, inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			err = logic.CreateNewTemplateVersionIfFleetValid(ctx, fleet)
			Expect(err).To(HaveOccurred())

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(*fleet.Status.Conditions).To(HaveLen(1))
			Expect((*fleet.Status.Conditions)[0].Type).To(Equal(api.FleetValid))
			Expect((*fleet.Status.Conditions)[0].Status).To(Equal(api.ConditionStatusFalse))
		})
	})

	When("a Repository definition is valid", func() {
		It("creates new TemplateVersions for relevant Fleets", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "repo", Kind: model.RepositoryKind}
			logic := tasks.NewFleetValidateLogic(taskManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpecification_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*goodGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.DeviceSpecification_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpecification_Config_Item{gitItem, inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			logic.ValidateFleetsReferencingRepository(ctx)

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(1))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(*fleet.Status.Conditions).To(HaveLen(1))
			Expect((*fleet.Status.Conditions)[0].Type).To(Equal(api.FleetValid))
			Expect((*fleet.Status.Conditions)[0].Status).To(Equal(api.ConditionStatusTrue))
		})

		It("doesn't create new TemplateVersions for irrelevant Fleets", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "repo", Kind: model.RepositoryKind}
			logic := tasks.NewFleetValidateLogic(taskManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpecification_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*badGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.DeviceSpecification_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpecification_Config_Item{gitItem, inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			logic.ValidateFleetsReferencingRepository(ctx)

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Status.Conditions).To(BeNil())
		})
	})

	When("a Repository definition is invalid", func() {
		It("doesn't create new TemplateVersions for relevant Fleets", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "missingrepo", Kind: model.RepositoryKind}
			logic := tasks.NewFleetValidateLogic(taskManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpecification_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*badGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.DeviceSpecification_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpecification_Config_Item{gitItem, inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			logic.ValidateFleetsReferencingRepository(ctx)

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(*fleet.Status.Conditions).To(HaveLen(1))
			Expect((*fleet.Status.Conditions)[0].Type).To(Equal(api.FleetValid))
			Expect((*fleet.Status.Conditions)[0].Status).To(Equal(api.ConditionStatusFalse))
		})
	})

	When("all repositories are deleted", func() {
		It("sets fleets that reference repositories as invalid", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Kind: model.RepositoryKind}
			logic := tasks.NewFleetValidateLogic(taskManager, log, storeInst, resourceRef)

			gitItem := api.DeviceSpecification_Config_Item{}
			err := gitItem.FromGitConfigProviderSpec(*badGitConfig)
			Expect(err).ToNot(HaveOccurred())

			inlineItem := api.DeviceSpecification_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpecification_Config_Item{gitItem, inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			logic.ValidateFleetsReferencingAnyRepository(ctx)

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			Expect(fleet.Status.Conditions).ToNot(BeNil())
			Expect(*fleet.Status.Conditions).To(HaveLen(1))
			Expect((*fleet.Status.Conditions)[0].Type).To(Equal(api.FleetValid))
			Expect((*fleet.Status.Conditions)[0].Status).To(Equal(api.ConditionStatusFalse))
		})

		It("has no effect on fleets that don't reference any repositories", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "repo", Kind: model.RepositoryKind}
			logic := tasks.NewFleetValidateLogic(taskManager, log, storeInst, resourceRef)

			inlineItem := api.DeviceSpecification_Config_Item{}
			err := inlineItem.FromInlineConfigProviderSpec(*goodInlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpecification_Config_Item{inlineItem}

			tvList, err := storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			_, err = storeInst.Fleet().Create(ctx, orgId, fleet, callback)
			Expect(err).ToNot(HaveOccurred())

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())

			logic.ValidateFleetsReferencingAnyRepository(ctx)

			tvList, err = storeInst.TemplateVersion().List(ctx, orgId, store.ListParams{})
			Expect(err).ToNot(HaveOccurred())
			Expect(tvList.Items).To(HaveLen(0))

			fleet, err = storeInst.Fleet().Get(ctx, orgId, "myfleet")
			Expect(err).ToNot(HaveOccurred())
			Expect(fleet.Status.Conditions).To(BeNil())
		})
	})

})
