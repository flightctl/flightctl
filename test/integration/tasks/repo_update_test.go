package tasks_test

import (
	"context"
	"encoding/json"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/service"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/tasks"
	"github.com/flightctl/flightctl/internal/tasks_client"
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
	var reference tasks_client.ResourceReference
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
		serviceHandler  *service.ServiceHandler
		cfg             *config.Config
		dbName          string
		callbackManager tasks_client.CallbackManager
		ctrl            *gomock.Controller
		mockPublisher   *queues.MockPublisher
	)

	BeforeEach(func() {
		ctx = context.WithValue(context.Background(), consts.InternalRequestCtxKey, true)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		storeInst, cfg, dbName, _ = store.PrepareDBForUnitTests(log)
		ctrl = gomock.NewController(GinkgoT())
		mockPublisher = queues.NewMockPublisher(ctrl)
		callbackManager = tasks_client.NewCallbackManager(mockPublisher, log)
		kvStore, err := kvstore.NewKVStore(ctx, log, "localhost", 6379, "adminpass")
		Expect(err).ToNot(HaveOccurred())
		serviceHandler = service.NewServiceHandler(storeInst, callbackManager, kvStore, nil, log, "", "")

		// Create 2 git config items, each to a different repo
		err = testutil.CreateRepositories(ctx, 2, storeInst, orgId)
		Expect(err).ToNot(HaveOccurred())

		gitConfig1 := &api.GitConfigProviderSpec{
			Name: "gitConfig1",
		}
		gitConfig1.GitRef.Path = "path"
		gitConfig1.GitRef.Repository = "myrepository-1"
		gitConfig1.GitRef.TargetRevision = "rev"
		gitConfig1.GitRef.MountPath = lo.ToPtr("/")
		gitItem1 := api.ConfigProviderSpec{}
		err = gitItem1.FromGitConfigProviderSpec(*gitConfig1)
		Expect(err).ToNot(HaveOccurred())

		gitConfig2 := &api.GitConfigProviderSpec{
			Name: "gitConfig2",
		}
		gitConfig1.GitRef.Path = "path"
		gitConfig1.GitRef.Repository = "myrepository-2"
		gitConfig1.GitRef.TargetRevision = "rev"
		gitConfig1.GitRef.MountPath = lo.ToPtr("/")
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

		fleetCallback := store.FleetStoreCallback(func(uuid.UUID, *api.Fleet, *api.Fleet) {})
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

		devCallback := store.DeviceStoreCallback(func(uuid.UUID, *api.Device, *api.Device) {})
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
		store.DeleteTestDB(log, cfg, storeInst, dbName)
	})

	When("a Repository definition is updated", func() {
		It("refreshes relevant fleets and devices", func() {
			resourceRef := tasks_client.ResourceReference{OrgID: orgId, Name: "myrepository-1", Kind: api.RepositoryKind}
			logic := tasks.NewRepositoryUpdateLogic(callbackManager, log, serviceHandler, resourceRef)
			mockPublisher.EXPECT().Publish(newResourceReferenceMatcher(tasks_client.FleetValidateTask, "fleet1")).Times(1)
			mockPublisher.EXPECT().Publish(newResourceReferenceMatcher(tasks_client.DeviceRenderTask, "device1")).Times(1)
			err := logic.HandleRepositoryUpdate(ctx)
			Expect(err).ToNot(HaveOccurred())

		})
	})

	When("all Repository definitions are deleted", func() {
		It("refreshes relevant fleets and devices", func() {
			resourceRef := tasks_client.ResourceReference{OrgID: orgId, Kind: api.RepositoryKind}
			logic := tasks.NewRepositoryUpdateLogic(callbackManager, log, serviceHandler, resourceRef)
			mockPublisher.EXPECT().Publish(newResourceReferenceMatcher(tasks_client.FleetValidateTask, "")).Times(2)
			mockPublisher.EXPECT().Publish(newResourceReferenceMatcher(tasks_client.DeviceRenderTask, "")).Times(2)
			err := logic.HandleAllRepositoriesDeleted(ctx, log)
			Expect(err).ToNot(HaveOccurred())

		})
	})
})
