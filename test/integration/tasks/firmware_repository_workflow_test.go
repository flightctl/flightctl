package tasks_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/kvstore"
	"github.com/flightctl/flightctl/internal/rendered"
	"github.com/flightctl/flightctl/internal/service/common"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	eventservice "github.com/flightctl/flightctl/internal/service/event"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	repositoryservice "github.com/flightctl/flightctl/internal/service/repository"
	templateversionservice "github.com/flightctl/flightctl/internal/service/templateversion"
	"github.com/flightctl/flightctl/internal/store"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	eventstore "github.com/flightctl/flightctl/internal/store/event"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	organizationstore "github.com/flightctl/flightctl/internal/store/organization"
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

// workflowConfigSchema is the closed updater configuration schema of the
// test-only release served by workflowDescriptors.
const workflowConfigSchema = `{
  "type": "object",
  "additionalProperties": false,
  "properties": {
    "resources": {"type": "array", "items": {"type": "object", "additionalProperties": false,
      "required": ["component", "targetGuid"],
      "properties": {"component": {"type": "string", "minLength": 1}, "targetGuid": {"type": "string", "format": "uuid"}}}},
    "releases": {"type": "array", "items": {"type": "object", "additionalProperties": false,
      "properties": {"artifact": {"type": "object", "additionalProperties": false,
        "properties": {"source": {"$ref": "#/$defs/source"}}}}}},
    "overlays": {"type": "array", "items": {"type": "object", "additionalProperties": false,
      "properties": {"artifact": {"type": "object", "additionalProperties": false,
        "properties": {"authentication": {"type": "object", "additionalProperties": false,
          "properties": {"signature": {"type": "object", "additionalProperties": false,
            "properties": {"source": {"$ref": "#/$defs/source"}}}}}}}}}}
  },
  "$defs": {"source": {"type": "object", "additionalProperties": false, "properties": {"repository": {"type": "string", "minLength": 1}}}}
}`

// workflowDescriptors approves one updater release whose configuration names
// an artifact repository and a signature repository.
type workflowDescriptors struct{}

func (workflowDescriptors) LookupRelease(_ context.Context, name, version string) (*common.ApprovedDescriptor, error) {
	if name != "nvidia-orin" || version != "0.1.0" {
		return nil, nil
	}
	return &common.ApprovedDescriptor{
		Name: name, Version: version, Approved: true,
		ConfigSchema: json.RawMessage(workflowConfigSchema),
		ReferencePaths: []common.FirmwareReferencePath{
			{Path: "releases[].artifact.source.repository", Kind: common.FirmwareReferenceArtifact},
			{Path: "overlays[].artifact.authentication.signature.source.repository", Kind: common.FirmwareReferenceSignature},
		},
		GUIDPaths:        []string{"resources[].targetGuid"},
		TemplatablePaths: []string{"resources[].targetGuid", "releases[].artifact.source.repository", "overlays[].artifact.authentication.signature.source.repository"},
	}, nil
}

func (workflowDescriptors) LookupSchemaByDigest(context.Context, string, string) (*common.RetainedSchemaEvidence, error) {
	return nil, nil
}

// workflowFirmware returns desired firmware whose updater configuration names
// artifactRepository and signatureRepository.
func workflowFirmware(artifactRepository, signatureRepository string) *api.FirmwareSpec {
	var spec api.FirmwareSpec
	Expect(json.Unmarshal([]byte(fmt.Sprintf(`{
  "profile": "orin-example-profile",
  "updater": {"name": "nvidia-orin", "version": "0.1.0", "config": {
    "resources": [{"component": "system", "targetGuid": "00000000-0000-4000-8000-000000000001"}],
    "releases": [{"artifact": {"source": {"repository": %q}}}],
    "overlays": [{"artifact": {"authentication": {"signature": {"source": {"repository": %q}}}}}]
  }},
  "components": [{"name": "system", "version": {"latest": true}}],
  "overlays": []
}`, artifactRepository, signatureRepository)), &spec)).To(Succeed())
	return &spec
}

var _ = Describe("Firmware repository references in the reconcile workflow", func() {
	var (
		log            *logrus.Logger
		ctx            context.Context
		orgId          uuid.UUID
		otherOrgId     uuid.UUID
		cfg            *config.Config
		dbName         string
		db             *gorm.DB
		deviceStore    devicestore.Store
		fleetStore     fleetstore.Store
		tvStore        templateversionstore.Store
		kvStore        kvstore.KVStore
		repositorySvc  repositoryservice.Service
		eventSvc       eventservice.Service
		deviceSvc      deviceservice.Service
		fleetSvc       fleetservice.Service
		tvSvc          templateversionservice.Service
		workerClient   worker_client.WorkerClient
		payloads       [][]byte
		configServer   *httptest.Server
		queuesProvider queues.Provider
	)

	BeforeEach(func() {
		ctx = testutil.StartSpecTracerForGinkgo(suiteCtx)
		orgId = store.NullOrgId
		log = flightlog.InitLogs()
		var err error
		cfg, dbName, db, err = testdb.CreateTestDB(ctx, log, "", store.InitDB)
		Expect(err).NotTo(HaveOccurred())
		otherOrgId = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, organizationstore.NewOrganizationStore(db), otherOrgId)).To(Succeed())
		deviceStore = devicestore.NewDeviceStore(db, log.WithField("pkg", "device-store"))
		fleetStore = fleetstore.NewFleetStore(db, log.WithField("pkg", "fleet-store"))
		tvStore = templateversionstore.NewTemplateVersionStore(db, log.WithField("pkg", "templateversion-store"))
		eventStore := eventstore.NewEventStore(db, log.WithField("pkg", "event-store"))
		payloads = nil
		producer := queues.NewMockQueueProducer(gomock.NewController(GinkgoT()))
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(func(_ context.Context, payload []byte, _ int64) error {
			payloads = append(payloads, append([]byte(nil), payload...))
			return nil
		}).AnyTimes()
		workerClient = worker_client.NewWorkerClient(producer, log, worker_client.WithDeltaPublisher(producer))
		kvStore, err = kvstore.NewKVStore(ctx, log, redisHost, redisPort, redisPassword)
		Expect(err).ToNot(HaveOccurred())
		eventsSvc := events.NewServiceHandler(eventStore, workerClient, log)
		eventSvc = eventservice.NewServiceHandler(eventStore, eventsSvc)
		repositorySvc = repositoryservice.NewServiceHandler(repositorystore.NewRepositoryStore(db, log.WithField("pkg", "repository-store")), eventsSvc, log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(deviceStore, nil, fleetStore, eventsSvc, kvStore, "", log,
			deviceservice.WithApprovedDescriptorProvider(workflowDescriptors{}), deviceservice.WithRepositoryGetter(repositorySvc.GetRepository))
		fleetSvc = fleetservice.NewServiceHandler(fleetStore, nil, eventsSvc, log,
			fleetservice.WithApprovedDescriptorProvider(workflowDescriptors{}), fleetservice.WithRepositoryGetter(repositorySvc.GetRepository))
		tvSvc = templateversionservice.NewServiceHandler(tvStore, kvStore, eventsSvc, log)
		configServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, err := w.Write([]byte("setting=value\n")); err != nil {
				log.WithError(err).Error("writing test repository response")
			}
		}))
		DeferCleanup(configServer.Close)
		if queuesProvider == nil {
			queuesProvider, err = queues.NewRedisProvider(ctx, log, fmt.Sprintf("firmware-workflow-test-%s", uuid.NewString()), redisHost, redisPort, redisPassword, queues.DefaultRetryConfig())
			Expect(err).ToNot(HaveOccurred())
			Expect(rendered.Bus.Initialize(ctx, kvStore, queuesProvider, 10*time.Second, log)).To(Succeed())
		}
	})

	AfterEach(func() {
		Expect(testdb.DeleteTestDB(ctx, log, cfg, db, dbName)).To(Succeed())
	})

	createRepository := func(org uuid.UUID, name string) {
		var spec api.RepositorySpec
		Expect(spec.FromHttpRepoSpec(api.HttpRepoSpec{Type: api.HttpRepoSpecTypeHttp, Url: configServer.URL})).To(Succeed())
		_, status := repositorySvc.CreateRepository(ctx, org, api.Repository{
			ApiVersion: "v1beta1", Kind: api.RepositoryKind, Metadata: api.ObjectMeta{Name: lo.ToPtr(name)}, Spec: spec,
		})
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)
	}
	queuedEvents := func() []worker_client.EventWithOrgId {
		queued := make([]worker_client.EventWithOrgId, 0, len(payloads))
		for _, payload := range payloads {
			var event worker_client.EventWithOrgId
			Expect(json.Unmarshal(payload, &event)).To(Succeed())
			queued = append(queued, event)
		}
		return queued
	}
	// updateRepository changes the repository through the repository service,
	// runs the repository update task on the event that the change queued, and
	// returns the resources that the task queued notifications for.
	updateRepository := func(org uuid.UUID, name string) []string {
		payloads = nil
		repository, status := repositorySvc.GetRepository(ctx, org, name)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		var spec api.RepositorySpec
		Expect(spec.FromHttpRepoSpec(api.HttpRepoSpec{Type: api.HttpRepoSpecTypeHttp, Url: configServer.URL + "/" + uuid.NewString()})).To(Succeed())
		repository.Spec = spec
		repository.Status = nil
		_, status = repositorySvc.ReplaceRepository(ctx, org, name, *repository)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		change, found := lo.Find(queuedEvents(), func(e worker_client.EventWithOrgId) bool {
			return e.Event.Reason == api.EventReasonResourceUpdated && e.Event.InvolvedObject.Kind == api.RepositoryKind && e.Event.InvolvedObject.Name == name
		})
		Expect(found).To(BeTrue(), "the repository change must queue an update event, queued: %v",
			lo.Map(queuedEvents(), func(e worker_client.EventWithOrgId, _ int) string {
				return string(e.Event.Reason) + " " + e.Event.InvolvedObject.Kind + "/" + e.Event.InvolvedObject.Name
			}))

		payloads = nil
		logic := tasks.NewRepositoryUpdateLogic(log, repositorySvc, eventSvc, change.OrgId, change.Event)
		Expect(logic.HandleRepositoryUpdate(ctx)).To(Succeed())
		var targets []string
		for _, queued := range queuedEvents() {
			if queued.Event.Reason == api.EventReasonReferencedRepositoryUpdated {
				Expect(queued.OrgId).To(Equal(org))
				targets = append(targets, queued.Event.InvolvedObject.Kind+"/"+queued.Event.InvolvedObject.Name)
			}
		}
		slices.Sort(targets)
		return targets
	}
	deviceRefs := func(org uuid.UUID, name string) []string {
		repos, err := deviceStore.GetRepositoryRefs(ctx, org, name)
		Expect(err).ToNot(HaveOccurred())
		names := lo.Map(repos.Items, func(r api.Repository, _ int) string { return lo.FromPtr(r.Metadata.Name) })
		slices.Sort(names)
		return names
	}
	fleetRefs := func(name string) []string {
		repos, err := fleetStore.GetRepositoryRefs(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		names := lo.Map(repos.Items, func(r api.Repository, _ int) string { return lo.FromPtr(r.Metadata.Name) })
		slices.Sort(names)
		return names
	}
	getDevice := func(name string) *api.Device {
		device, err := deviceStore.Get(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		return device
	}
	httpConfig := func(repository string) *[]api.ConfigProviderSpec {
		provider := api.HttpConfigProviderSpec{Name: "app-config"}
		provider.HttpRef.Repository = repository
		provider.HttpRef.FilePath = "/etc/app.conf"
		item := api.ConfigProviderSpec{}
		Expect(item.FromHttpConfigProviderSpec(provider)).To(Succeed())
		return &[]api.ConfigProviderSpec{item}
	}
	render := func(name string, reason api.EventReason) {
		event := api.Event{Reason: reason, InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: name}}
		Expect(tasks.NewDeviceRenderLogic(log, deviceSvc, repositorySvc, nil, nil, kvStore, nil).RenderDevice(ctx, orgId, event)).To(Succeed())
	}
	specValid := func(device *api.Device) *api.Condition {
		return api.FindStatusCondition(device.Status.Conditions, api.ConditionTypeDeviceSpecValid)
	}
	renderedVersion := func(device *api.Device) string {
		return lo.FromPtr(device.Metadata.Annotations)[api.DeviceAnnotationRenderedVersion]
	}
	validateFleet := func(name string, reason api.EventReason) error {
		event := api.Event{Reason: reason, InvolvedObject: api.ObjectReference{Kind: api.FleetKind, Name: name}}
		logic := tasks.NewFleetValidateLogic(log, fleetSvc, tvSvc, deviceSvc, repositorySvc, nil, orgId, event)
		logic.WorkerClient = workerClient
		return logic.CreateNewTemplateVersionIfFleetValid(ctx)
	}
	fleetValid := func(name string) *api.Condition {
		fleet, err := fleetStore.Get(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		return api.FindStatusCondition(fleet.Status.Conditions, api.ConditionTypeFleetValid)
	}
	templateVersionCount := func(fleetName string) int {
		list, err := tvStore.List(ctx, orgId, store.ListParams{})
		Expect(err).ToNot(HaveOccurred())
		return len(lo.Filter(list.Items, func(tv api.TemplateVersion, _ int) bool { return tv.Spec.Fleet == fleetName }))
	}
	repositoryRowExists := func(name string) bool {
		var count int64
		Expect(db.WithContext(ctx).Table("repositories").Where("org_id = ? AND name = ?", orgId, name).Count(&count).Error).To(Succeed())
		return count > 0
	}

	It("When a standalone device mixes configuration and firmware repositories it should keep the complete set through writes, rendering, notifications and withdrawal", func() {
		for _, name := range []string{"shared", "art-f", "sig-s", "sig-t"} {
			createRepository(orgId, name)
		}
		createRepository(otherOrgId, "sig-t")

		By("creating the device with the shared repository in its configuration and firmware")
		_, status := deviceSvc.CreateDevice(ctx, orgId, api.Device{ApiVersion: "v1beta1", Kind: api.DeviceKind,
			Metadata: api.ObjectMeta{Name: lo.ToPtr("device-1")}, Spec: &api.DeviceSpec{Config: httpConfig("shared"), Firmware: workflowFirmware("shared", "sig-s")}})
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)
		Expect(deviceRefs(orgId, "device-1")).To(Equal([]string{"shared", "sig-s"}))
		_, status = deviceSvc.CreateDevice(ctx, otherOrgId, api.Device{ApiVersion: "v1beta1", Kind: api.DeviceKind,
			Metadata: api.ObjectMeta{Name: lo.ToPtr("other-device")}, Spec: &api.DeviceSpec{Firmware: workflowFirmware("sig-t", "sig-t")}})
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)
		Expect(deviceRefs(otherOrgId, "other-device")).To(Equal([]string{"sig-t"}))

		By("replacing the artifact repository while the configuration keeps the shared one")
		_, status = deviceSvc.ReplaceDevice(ctx, orgId, "device-1", api.Device{ApiVersion: "v1beta1", Kind: api.DeviceKind,
			Metadata: api.ObjectMeta{Name: lo.ToPtr("device-1")}, Spec: &api.DeviceSpec{Config: httpConfig("shared"), Firmware: workflowFirmware("art-f", "sig-s")}}, nil, true, true)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		Expect(deviceRefs(orgId, "device-1")).To(Equal([]string{"art-f", "shared", "sig-s"}))

		By("patching the signature repository, which drops the firmware-only reference")
		_, status = deviceSvc.PatchDevice(ctx, orgId, "device-1", api.PatchRequest{{Op: "replace",
			Path: "/spec/firmware/updater/config/overlays/0/artifact/authentication/signature/source/repository", Value: "sig-t"}}, true, true)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		Expect(deviceRefs(orgId, "device-1")).To(Equal([]string{"art-f", "shared", "sig-t"}))

		By("patching only the configuration, which keeps the firmware references")
		_, status = deviceSvc.PatchDevice(ctx, orgId, "device-1", api.PatchRequest{{Op: "replace", Path: "/spec/config/0/httpRef/filePath", Value: "/etc/app2.conf"}}, true, true)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		Expect(deviceRefs(orgId, "device-1")).To(Equal([]string{"art-f", "shared", "sig-t"}))

		By("rendering, which holds the device and writes the same complete set")
		render("device-1", api.EventReasonResourceUpdated)
		held := getDevice("device-1")
		Expect(specValid(held).Reason).To(Equal(api.DeviceSpecValidReasonFirmwareUnsupported))
		Expect(renderedVersion(held)).To(BeEmpty())
		Expect(deviceRefs(orgId, "device-1")).To(Equal([]string{"art-f", "shared", "sig-t"}))

		By("updating a firmware-only repository, which notifies the device but not the device of another organization")
		Expect(updateRepository(orgId, "sig-t")).To(Equal([]string{"Device/device-1"}))
		Expect(updateRepository(orgId, "art-f")).To(Equal([]string{"Device/device-1"}))
		Expect(updateRepository(orgId, "sig-s")).To(BeEmpty(), "a reference removed by the patch must not notify")
		Expect(updateRepository(otherOrgId, "sig-t")).To(Equal([]string{"Device/other-device"}))

		By("rendering for the notification, which keeps the device held")
		render("device-1", api.EventReasonReferencedRepositoryUpdated)
		stillHeld := getDevice("device-1")
		Expect(specValid(stillHeld).Reason).To(Equal(api.DeviceSpecValidReasonFirmwareUnsupported))
		Expect(renderedVersion(stillHeld)).To(BeEmpty())
		Expect(stillHeld.Metadata.ResourceVersion).To(Equal(held.Metadata.ResourceVersion))
		Expect(deviceRefs(orgId, "device-1")).To(Equal([]string{"art-f", "shared", "sig-t"}))

		By("withdrawing the firmware, which keeps only the configuration reference")
		_, status = deviceSvc.PatchDevice(ctx, orgId, "device-1", api.PatchRequest{{Op: "remove", Path: "/spec/firmware"}}, true, true)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		Expect(deviceRefs(orgId, "device-1")).To(Equal([]string{"shared"}))
		Expect(updateRepository(orgId, "sig-t")).To(BeEmpty())
		Expect(updateRepository(orgId, "art-f")).To(BeEmpty())
		Expect(updateRepository(orgId, "shared")).To(Equal([]string{"Device/device-1"}))

		By("rendering after the withdrawal, which renders the device normally")
		render("device-1", api.EventReasonResourceUpdated)
		resumed := getDevice("device-1")
		Expect(specValid(resumed).Status).To(Equal(api.ConditionStatusTrue), specValid(resumed).Message)
		Expect(renderedVersion(resumed)).To(Equal("1"))
		Expect(deviceRefs(orgId, "device-1")).To(Equal([]string{"shared"}))

		By("deleting the device, which removes its references")
		Expect(deviceSvc.DeleteDevice(ctx, orgId, "device-1").Code).To(BeEquivalentTo(http.StatusOK))
		Expect(updateRepository(orgId, "shared")).To(BeEmpty())
	})

	It("When the descriptor provider is unavailable a render of a device with firmware should hold it and leave its references unchanged", func() {
		for _, name := range []string{"shared", "sig-s"} {
			createRepository(orgId, name)
		}
		status := api.NewDeviceStatus()
		_, err := deviceStore.Create(ctx, orgId, &api.Device{Metadata: api.ObjectMeta{Name: lo.ToPtr("device-1")},
			Spec: &api.DeviceSpec{Config: httpConfig("shared"), Firmware: workflowFirmware("shared", "sig-s")}, Status: &status}, nil)
		Expect(err).ToNot(HaveOccurred())
		Expect(deviceStore.OverwriteRepositoryRefs(ctx, orgId, "device-1", "shared", "sig-s")).To(Succeed())
		unavailable := deviceservice.NewDeviceServiceHandler(deviceStore, nil, fleetStore, events.NewServiceHandler(eventstore.NewEventStore(db, log), workerClient, log), kvStore, "", log)

		event := api.Event{Reason: api.EventReasonResourceUpdated, InvolvedObject: api.ObjectReference{Kind: api.DeviceKind, Name: "device-1"}}
		Expect(tasks.NewDeviceRenderLogic(log, unavailable, repositorySvc, nil, nil, kvStore, nil).RenderDevice(ctx, orgId, event)).To(Succeed())

		held := getDevice("device-1")
		Expect(specValid(held).Reason).To(Equal(api.DeviceSpecValidReasonFirmwareUnsupported))
		Expect(renderedVersion(held)).To(BeEmpty())
		Expect(deviceRefs(orgId, "device-1")).To(Equal([]string{"shared", "sig-s"}))
	})

	It("When a fleet template carries firmware it should get its references only from fleet validation and stay held when they change", func() {
		for _, name := range []string{"shared", "art-a", "sig-s"} {
			createRepository(orgId, name)
		}
		newFleet := func(name string, firmware *api.FirmwareSpec) api.Fleet {
			fleet := api.Fleet{ApiVersion: "v1beta1", Kind: api.FleetKind, Metadata: api.ObjectMeta{Name: lo.ToPtr(name)},
				Spec: api.FleetSpec{Selector: &api.LabelSelector{MatchLabels: &map[string]string{"site": name}}}}
			fleet.Spec.Template.Spec = api.DeviceSpec{Config: httpConfig("shared"), Firmware: firmware}
			return fleet
		}

		By("creating the fleet, which stores no references before fleet validation")
		_, status := fleetSvc.CreateFleet(ctx, orgId, newFleet("fleet-1", workflowFirmware("art-a", "sig-s")))
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)
		Expect(fleetRefs("fleet-1")).To(BeEmpty())

		By("validating the fleet, which stores the complete set and holds the template")
		Expect(validateFleet("fleet-1", api.EventReasonResourceCreated)).To(Succeed())
		Expect(fleetRefs("fleet-1")).To(Equal([]string{"art-a", "shared", "sig-s"}))
		Expect(fleetValid("fleet-1").Reason).To(Equal(api.FleetValidReasonFirmwareRenderingUnsupported))
		Expect(templateVersionCount("fleet-1")).To(BeZero())

		By("updating a firmware-only repository, which notifies the fleet, whose validation keeps it held")
		Expect(updateRepository(orgId, "sig-s")).To(Equal([]string{"Fleet/fleet-1"}))
		Expect(validateFleet("fleet-1", api.EventReasonReferencedRepositoryUpdated)).To(Succeed())
		Expect(fleetValid("fleet-1").Reason).To(Equal(api.FleetValidReasonFirmwareRenderingUnsupported))
		Expect(templateVersionCount("fleet-1")).To(BeZero())
		Expect(fleetRefs("fleet-1")).To(Equal([]string{"art-a", "shared", "sig-s"}))

		By("validating a template with a templated signature repository, which stores no reference for it")
		templated := workflowFirmware("art-a", "{{ .metadata.labels.signatures }}")
		_, status = fleetSvc.CreateFleet(ctx, orgId, newFleet("fleet-templated-ref", templated))
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)
		Expect(validateFleet("fleet-templated-ref", api.EventReasonResourceCreated)).To(Succeed())
		Expect(fleetRefs("fleet-templated-ref")).To(Equal([]string{"art-a", "shared"}))
		Expect(repositoryRowExists("{{ .metadata.labels.signatures }}")).To(BeFalse())

		By("validating a template with a templated updater identity, which stores only configuration references")
		templatedIdentity := workflowFirmware("art-a", "sig-s")
		templatedIdentity.Updater.Version = "{{ .metadata.labels.updaterVersion }}"
		_, status = fleetSvc.CreateFleet(ctx, orgId, newFleet("fleet-templated-identity", templatedIdentity))
		Expect(status.Code).To(BeEquivalentTo(http.StatusCreated), status.Message)
		Expect(validateFleet("fleet-templated-identity", api.EventReasonResourceCreated)).To(Succeed())
		Expect(fleetRefs("fleet-templated-identity")).To(Equal([]string{"shared"}))
		Expect(fleetValid("fleet-templated-identity").Reason).To(Equal(api.FleetValidReasonFirmwareRenderingUnsupported))

		By("withdrawing the firmware, which lets validation create a template version and keeps the configuration reference")
		_, status = fleetSvc.PatchFleet(ctx, orgId, "fleet-1", api.PatchRequest{{Op: "remove", Path: "/spec/template/spec/firmware"}}, false)
		Expect(status.Code).To(BeEquivalentTo(http.StatusOK), status.Message)
		Expect(validateFleet("fleet-1", api.EventReasonResourceUpdated)).To(Succeed())
		Expect(fleetValid("fleet-1").Status).To(Equal(api.ConditionStatusTrue))
		Expect(templateVersionCount("fleet-1")).To(Equal(1))
		Expect(fleetRefs("fleet-1")).To(Equal([]string{"shared"}))
		Expect(updateRepository(orgId, "sig-s")).To(BeEmpty())
		Expect(updateRepository(orgId, "art-a")).To(Equal([]string{"Fleet/fleet-templated-ref"}))
	})
})
