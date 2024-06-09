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
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
)

var _ = Describe("RepoUpdate", func() {
	var (
		log         *logrus.Logger
		ctx         context.Context
		orgId       uuid.UUID
		storeInst   store.Store
		cfg         *config.Config
		dbName      string
		taskManager tasks.TaskManager
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		storeInst, cfg, dbName = store.PrepareDBForUnitTests(log)
		taskManager = tasks.Init(log, storeInst)

		// Create 2 git config items, each to a different repo
		err := testutil.CreateRepositories(ctx, 2, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())

		gitConfig1 := &api.GitConfigProviderSpec{
			ConfigType: string(api.TemplateDiscriminatorGitConfig),
			Name:       "gitConfig1",
		}
		gitConfig1.GitRef.Path = "path"
		gitConfig1.GitRef.Repository = "myrepository-1"
		gitConfig1.GitRef.TargetRevision = "rev"
		gitItem1 := api.DeviceSpec_Config_Item{}
		err = gitItem1.FromGitConfigProviderSpec(*gitConfig1)
		Expect(err).ToNot(HaveOccurred())

		gitConfig2 := &api.GitConfigProviderSpec{
			ConfigType: string(api.TemplateDiscriminatorGitConfig),
			Name:       "gitConfig2",
		}
		gitConfig1.GitRef.Path = "path"
		gitConfig1.GitRef.Repository = "myrepository-2"
		gitConfig1.GitRef.TargetRevision = "rev"
		gitItem2 := api.DeviceSpec_Config_Item{}
		err = gitItem2.FromGitConfigProviderSpec(*gitConfig2)
		Expect(err).ToNot(HaveOccurred())

		// Create an inline config item
		inlineConfig := &api.InlineConfigProviderSpec{
			ConfigType: string(api.TemplateDiscriminatorInlineConfig),
			Name:       "inlineConfig",
		}
		var goodInline map[string]interface{}
		err = json.Unmarshal([]byte("{\"ignition\": {\"version\": \"3.4.0\"}}"), &goodInline)
		Expect(err).ToNot(HaveOccurred())
		inlineConfig.Inline = goodInline
		inlineItem := api.DeviceSpec_Config_Item{}
		err = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)
		Expect(err).ToNot(HaveOccurred())

		config1 := []api.DeviceSpec_Config_Item{gitItem1, inlineItem}
		config2 := []api.DeviceSpec_Config_Item{gitItem2, inlineItem}

		// Create fleet1 referencing repo1, fleet2 referencing repo2
		fleet1 := api.Fleet{
			Metadata: api.ObjectMeta{Name: util.StrToPtr("fleet1")},
			Spec:     api.FleetSpec{},
		}
		fleet1.Spec.Template.Spec = api.DeviceSpec{Config: &config1}

		fleet2 := api.Fleet{
			Metadata: api.ObjectMeta{Name: util.StrToPtr("fleet2")},
			Spec:     api.FleetSpec{},
		}
		fleet2.Spec.Template.Spec = api.DeviceSpec{Config: &config2}

		fleetCallback := store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {})
		_, err = storeInst.Fleet().Create(ctx, orgId, &fleet1, fleetCallback)
		Expect(err).ToNot(HaveOccurred())
		err = storeInst.Fleet().OverwriteRepositoryRefs(ctx, orgId, "fleet1", "myrepository-1")
		Expect(err).ToNot(HaveOccurred())
		_, err = storeInst.Fleet().Create(ctx, orgId, &fleet2, fleetCallback)
		Expect(err).ToNot(HaveOccurred())
		err = storeInst.Fleet().OverwriteRepositoryRefs(ctx, orgId, "fleet2", "myrepository-2")
		Expect(err).ToNot(HaveOccurred())

		// Create device1 referencing repo1, device2 referencing repo2
		device1 := api.Device{
			Metadata: api.ObjectMeta{Name: util.StrToPtr("device1")},
			Spec: &api.DeviceSpec{
				Config: &config1,
			},
		}

		device2 := api.Device{
			Metadata: api.ObjectMeta{Name: util.StrToPtr("device2")},
			Spec: &api.DeviceSpec{
				Config: &config2,
			},
		}

		devCallback := store.DeviceStoreCallback(func(before *model.Device, after *model.Device) {})
		_, err = storeInst.Device().Create(ctx, orgId, &device1, devCallback)
		Expect(err).ToNot(HaveOccurred())
		err = storeInst.Device().OverwriteRepositoryRefs(ctx, orgId, "device1", "myrepository-1")
		Expect(err).ToNot(HaveOccurred())
		_, err = storeInst.Device().Create(ctx, orgId, &device2, devCallback)
		Expect(err).ToNot(HaveOccurred())
		err = storeInst.Device().OverwriteRepositoryRefs(ctx, orgId, "device2", "myrepository-2")
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	When("a Repository definition is updated", func() {
		It("refreshes relevant fleets and devices", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "myrepository-1", Kind: model.RepositoryKind}
			logic := tasks.NewRepositoryUpdateLogic(taskManager, log, storeInst, resourceRef)
			err := logic.HandleRepositoryUpdate(ctx)
			Expect(err).ToNot(HaveOccurred())

			fleetRef := taskManager.GetTask(tasks.ChannelFleetValidate)
			Expect(fleetRef.Name).To(Equal("fleet1"))
			Expect(taskManager.HasTasks(tasks.ChannelFleetValidate)).To(BeFalse())
			devRef := taskManager.GetTask(tasks.ChannelDeviceRender)
			Expect(devRef.Name).To(Equal("device1"))
			Expect(taskManager.HasTasks(tasks.ChannelDeviceRender)).To(BeFalse())
		})
	})

	When("all Repository definitions are deleted", func() {
		It("refreshes relevant fleets and devices", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Kind: model.RepositoryKind}
			logic := tasks.NewRepositoryUpdateLogic(taskManager, log, storeInst, resourceRef)
			err := logic.HandleAllRepositoriesDeleted(ctx, log)
			Expect(err).ToNot(HaveOccurred())

			// both fleets and both devices
			_ = taskManager.GetTask(tasks.ChannelFleetValidate)
			_ = taskManager.GetTask(tasks.ChannelFleetValidate)
			Expect(taskManager.HasTasks(tasks.ChannelFleetValidate)).To(BeFalse())
			_ = taskManager.GetTask(tasks.ChannelDeviceRender)
			_ = taskManager.GetTask(tasks.ChannelDeviceRender)
			Expect(taskManager.HasTasks(tasks.ChannelDeviceRender)).To(BeFalse())
		})
	})
})
