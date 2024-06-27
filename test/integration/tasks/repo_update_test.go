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
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/sirupsen/logrus"
	"go.uber.org/mock/gomock"
)

type resourceReferenceMatcher struct {
	taskName string
	name     string
}

func newResourceReferenceMatcher(taskName, name string) gomock.Matcher {
	return &resourceReferenceMatcher{
		taskName: taskName,
		name:     name,
	}
}

func (r *resourceReferenceMatcher) Matches(param any) bool {
	b, ok := param.([]byte)
	if !ok {
		return false
	}
	var reference tasks.ResourceReference
	if err := json.Unmarshal(b, &reference); err != nil {
		return false
	}
	if r.taskName != reference.TaskName {
		return false
	}
	return r.name == reference.Name || r.name == ""
}

func (r *resourceReferenceMatcher) String() string {
	return "resource-reference-matcher"
}

var _ = Describe("RepoUpdate", func() {
	var (
		log             *logrus.Logger
		ctx             context.Context
		orgId           uuid.UUID
		storeInst       store.Store
		cfg             *config.Config
		dbName          string
		callbackManager tasks.CallbackManager
		ctrl            *gomock.Controller
		mockPublisher   *queues.MockPublisher
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		storeInst, cfg, dbName = store.PrepareDBForUnitTests(log)
		ctrl = gomock.NewController(GinkgoT())
		mockPublisher = queues.NewMockPublisher(ctrl)
		callbackManager = tasks.NewCallbackManager(mockPublisher, log)

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
		ctrl.Finish()
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	When("a Repository definition is updated", func() {
		It("refreshes relevant fleets and devices", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Name: "myrepository-1", Kind: model.RepositoryKind}
			logic := tasks.NewRepositoryUpdateLogic(callbackManager, log, storeInst, resourceRef)
			mockPublisher.EXPECT().Publish(newResourceReferenceMatcher(tasks.FleetValidateTask, "fleet1")).Times(1)
			mockPublisher.EXPECT().Publish(newResourceReferenceMatcher(tasks.DeviceRenderTask, "device1")).Times(1)
			err := logic.HandleRepositoryUpdate(ctx)
			Expect(err).ToNot(HaveOccurred())

		})
	})

	When("all Repository definitions are deleted", func() {
		It("refreshes relevant fleets and devices", func() {
			resourceRef := tasks.ResourceReference{OrgID: orgId, Kind: model.RepositoryKind}
			logic := tasks.NewRepositoryUpdateLogic(callbackManager, log, storeInst, resourceRef)
			mockPublisher.EXPECT().Publish(newResourceReferenceMatcher(tasks.FleetValidateTask, "")).Times(2)
			mockPublisher.EXPECT().Publish(newResourceReferenceMatcher(tasks.DeviceRenderTask, "")).Times(2)
			err := logic.HandleAllRepositoriesDeleted(ctx, log)
			Expect(err).ToNot(HaveOccurred())

		})
	})
})
