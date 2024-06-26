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

var _ = Describe("TVPopulate", func() {
	var (
		log           *logrus.Logger
		ctx           context.Context
		orgId         uuid.UUID
		storeInst     store.Store
		cfg           *config.Config
		dbName        string
		taskManager   tasks.TaskManager
		fleet         *api.Fleet
		tv            *api.TemplateVersion
		fleetCallback store.FleetStoreCallback
	)

	BeforeEach(func() {
		ctx = context.Background()
		orgId, _ = uuid.NewUUID()
		log = flightlog.InitLogs()
		storeInst, cfg, dbName = store.PrepareDBForUnitTests(log)
		taskManager = tasks.Init(log, storeInst)
		fleetCallback = store.FleetStoreCallback(func(before *model.Fleet, after *model.Fleet) {})

		fleet = &api.Fleet{
			Metadata: api.ObjectMeta{Name: util.StrToPtr("fleet")},
			Spec:     api.FleetSpec{},
		}
		_, err := storeInst.Fleet().Create(ctx, orgId, fleet, fleetCallback)
		Expect(err).ToNot(HaveOccurred())

		testutil.CreateTestDevices(ctx, 2, storeInst.Device(), orgId, util.SetResourceOwner(model.FleetKind, *fleet.Metadata.Name), false)

		tv = &api.TemplateVersion{
			Metadata: api.ObjectMeta{
				Name:  util.StrToPtr("tv"),
				Owner: util.SetResourceOwner(model.FleetKind, *fleet.Metadata.Name),
			},
			Spec: api.TemplateVersionSpec{Fleet: *fleet.Metadata.Name},
		}
		tvCallback := store.TemplateVersionStoreCallback(func(tv *model.TemplateVersion) {})
		_, err = storeInst.TemplateVersion().Create(ctx, orgId, tv, tvCallback)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		store.DeleteTestDB(cfg, storeInst, dbName)
	})

	When("a template has a valid inline config with no params", func() {
		It("copies the config as is", func() {
			inlineConfig := &api.InlineConfigProviderSpec{
				ConfigType: string(api.TemplateDiscriminatorInlineConfig),
				Name:       "inlineConfig",
			}
			var inline map[string]interface{}
			err := json.Unmarshal([]byte("{\"ignition\": {\"version\": \"3.4.0\"}}"), &inline)
			Expect(err).ToNot(HaveOccurred())
			inlineConfig.Inline = inline

			inlineItem := api.DeviceSpec_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpec_Config_Item{inlineItem}
			_, _, err = storeInst.Fleet().CreateOrUpdate(ctx, orgId, fleet, fleetCallback)
			Expect(err).ToNot(HaveOccurred())

			owner := util.SetResourceOwner(model.FleetKind, *fleet.Metadata.Name)
			resourceRef := tasks.ResourceReference{OrgID: orgId, Op: tasks.TemplateVersionPopulateOpCreated, Name: "tv", Kind: model.TemplateVersionKind, Owner: *owner}
			logic := tasks.NewTemplateVersionPopulateLogic(taskManager, log, storeInst, resourceRef)
			err = logic.SyncFleetTemplateToTemplateVersion(ctx)
			Expect(err).ToNot(HaveOccurred())

			tv, err = storeInst.TemplateVersion().Get(ctx, orgId, *fleet.Metadata.Name, *tv.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())

			Expect(tv.Status.Config).ToNot(BeNil())
			Expect(*tv.Status.Config).To(HaveLen(1))
			configItem := (*tv.Status.Config)[0]
			newInline, err := configItem.AsInlineConfigProviderSpec()
			Expect(err).ToNot(HaveOccurred())

			Expect(newInline.Inline).To(Equal(inline))
		})
	})

	When("a template has a valid inline config with params", func() {
		It("copies the config as is", func() {
			inlineConfig := &api.InlineConfigProviderSpec{
				ConfigType: string(api.TemplateDiscriminatorInlineConfig),
				Name:       "inlineConfig",
			}
			var inline map[string]interface{}
			err := json.Unmarshal([]byte("{\"ignition\":{\"version\":\"3.4.0\"},\"storage\":{\"files\":[{\"overwrite\":true,\"path\":\"/etc/motd\",\"contents\":{\"source\":\"data:,{{ device.metadata.labels[key] }}\"},\"mode\":422}]}}"), &inline)
			Expect(err).ToNot(HaveOccurred())
			inlineConfig.Inline = inline

			inlineItem := api.DeviceSpec_Config_Item{}
			err = inlineItem.FromInlineConfigProviderSpec(*inlineConfig)
			Expect(err).ToNot(HaveOccurred())

			fleet.Spec.Template.Spec.Config = &[]api.DeviceSpec_Config_Item{inlineItem}
			_, _, err = storeInst.Fleet().CreateOrUpdate(ctx, orgId, fleet, fleetCallback)
			Expect(err).ToNot(HaveOccurred())

			owner := util.SetResourceOwner(model.FleetKind, *fleet.Metadata.Name)
			resourceRef := tasks.ResourceReference{OrgID: orgId, Op: tasks.TemplateVersionPopulateOpCreated, Name: "tv", Kind: model.TemplateVersionKind, Owner: *owner}
			logic := tasks.NewTemplateVersionPopulateLogic(taskManager, log, storeInst, resourceRef)
			err = logic.SyncFleetTemplateToTemplateVersion(ctx)
			Expect(err).ToNot(HaveOccurred())

			tv, err = storeInst.TemplateVersion().Get(ctx, orgId, *fleet.Metadata.Name, *tv.Metadata.Name)
			Expect(err).ToNot(HaveOccurred())

			Expect(tv.Status.Config).ToNot(BeNil())
			Expect(*tv.Status.Config).To(HaveLen(1))
			configItem := (*tv.Status.Config)[0]
			newInline, err := configItem.AsInlineConfigProviderSpec()
			Expect(err).ToNot(HaveOccurred())

			Expect(newInline.Inline).To(Equal(inline))
		})
	})
})
