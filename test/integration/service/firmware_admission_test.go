package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	convertv1beta1 "github.com/flightctl/flightctl/internal/api/convert/v1beta1"
	"github.com/flightctl/flightctl/internal/api/server"
	apiserver "github.com/flightctl/flightctl/internal/api_server"
	"github.com/flightctl/flightctl/internal/api_server/versioning"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/service/common"
	deviceservice "github.com/flightctl/flightctl/internal/service/device"
	"github.com/flightctl/flightctl/internal/service/events"
	fleetservice "github.com/flightctl/flightctl/internal/service/fleet"
	devicestore "github.com/flightctl/flightctl/internal/store/device"
	fleetstore "github.com/flightctl/flightctl/internal/store/fleet"
	transportv1beta1 "github.com/flightctl/flightctl/internal/transport/v1beta1"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/internal/worker_client"
	"github.com/flightctl/flightctl/pkg/queues"
	testutil "github.com/flightctl/flightctl/test/util"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"go.uber.org/mock/gomock"
	"gorm.io/gorm"
)

// firmwareConfigSchema is a closed updater configuration schema for the
// test-only release served by firmwareDescriptors.
const firmwareConfigSchema = `{
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

const firmwareJSON = `{
  "profile": "orin-example-profile",
  "updater": {"name": "nvidia-orin", "version": "0.1.0", "config": {
    "resources": [{"component": "system", "targetGuid": "00000000-0000-4000-8000-000000000001"},
                  {"component": "camera", "targetGuid": "00000000-0000-4000-8000-000000000002"}],
    "releases": [{"artifact": {"source": {"repository": "firmware-artifacts"}}}],
    "overlays": [{"artifact": {"authentication": {"signature": {"source": {"repository": "firmware-signatures"}}}}}]
  }},
  "components": [{"name": "system", "version": {"latest": true}}],
  "overlays": []
}`

// fullFirmwareJSON sets every desired firmware field. Version values are
// vendor strings that the server stores without ordering them, so the camera
// minimum is deliberately above its maximum in plain string order.
const fullFirmwareJSON = `{
  "profile": "orin-example-profile",
  "retryGeneration": 3,
  "updater": {"name": "nvidia-orin", "version": "0.1.0", "config": {
    "resources": [{"component": "system", "targetGuid": "00000000-0000-4000-8000-000000000001"},
                  {"component": "camera", "targetGuid": "00000000-0000-4000-8000-000000000002"}],
    "releases": [{"artifact": {"source": {"repository": "firmware-artifacts"}}}],
    "overlays": [{"artifact": {"authentication": {"signature": {"source": {"repository": "firmware-signatures"}}}}}]
  }},
  "components": [
    {"name": "system", "version": {"exact": "36.4.4-rc.1+build.7"}, "artifactDigest": "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
    {"name": "camera", "version": {"minimum": "1.10.0", "maximum": "1.2.0"}},
    {"name": "uefi", "version": {"latest": true, "minimum": "2024.05"}}
  ],
  "overlays": [{"name": "camera-overlay", "compatibleFirmware": [
    {"component": "camera", "minimum": "1.2.0", "maximum": "1.10.0"},
    {"component": "system", "exact": "36.4.4-rc.1+build.7"}]}]
}`

// firmwareDescriptors is a test stub of the approved descriptor provider that
// serves one release and counts its lookups.
type firmwareDescriptors struct {
	lookups atomic.Int32
}

func (p *firmwareDescriptors) LookupRelease(_ context.Context, name, version string) (*common.ApprovedDescriptor, error) {
	p.lookups.Add(1)
	if name != "nvidia-orin" || version != "0.1.0" {
		return nil, nil
	}
	return &common.ApprovedDescriptor{
		Name: name, Version: version, Approved: true,
		ConfigSchema: json.RawMessage(firmwareConfigSchema),
		ReferencePaths: []common.FirmwareReferencePath{
			{Path: "releases[].artifact.source.repository", Kind: common.FirmwareReferenceArtifact},
			{Path: "overlays[].artifact.authentication.signature.source.repository", Kind: common.FirmwareReferenceSignature},
		},
		GUIDPaths:        []string{"resources[].targetGuid"},
		TemplatablePaths: []string{"resources[].targetGuid", "releases[].artifact.source.repository", "overlays[].artifact.authentication.signature.source.repository"},
	}, nil
}

func (p *firmwareDescriptors) LookupSchemaByDigest(context.Context, string, string) (*common.RetainedSchemaEvidence, error) {
	return nil, errors.New("retained schema evidence is not used by admission")
}

var _ = Describe("Firmware admission at the write boundary", func() {
	var (
		suite       *ServiceTestSuite
		ctx         context.Context
		orgId       uuid.UUID
		otherOrgId  uuid.UUID
		descriptors *firmwareDescriptors
		devStore    *devicestore.DeviceStore
		fleetStore  *fleetstore.FleetStore
		deviceSvc   deviceservice.Service
		fleetSvc    fleetservice.Service
	)

	createHTTPRepository := func(org uuid.UUID, name string) {
		var spec api.RepositorySpec
		Expect(spec.FromHttpRepoSpec(api.HttpRepoSpec{Type: api.HttpRepoSpecTypeHttp, Url: "https://artifacts.example.com/" + name})).To(Succeed())
		_, status := suite.Repository.CreateRepository(ctx, org, api.Repository{
			ApiVersion: "v1beta1", Kind: api.RepositoryKind,
			Metadata: api.ObjectMeta{Name: lo.ToPtr(name)},
			Spec:     spec,
		})
		Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
	}

	BeforeEach(func() {
		suite = NewServiceTestSuite()
		suite.Setup()
		ctx = suite.Ctx
		orgId = suite.OrgID
		otherOrgId = uuid.New()
		Expect(testutil.CreateTestOrganization(ctx, suite.OrganizationStore, otherOrgId)).To(Succeed())
		createHTTPRepository(orgId, "firmware-artifacts")
		createHTTPRepository(orgId, "firmware-signatures")
		createHTTPRepository(orgId, "config-http")
		createHTTPRepository(otherOrgId, "other-signatures")

		descriptors = &firmwareDescriptors{}
		devStore = devicestore.NewDeviceStore(suite.DB, suite.Log.WithField("pkg", "device-store"))
		fleetStore = fleetstore.NewFleetStore(suite.DB, suite.Log.WithField("pkg", "fleet-store"))
		ctrl := gomock.NewController(GinkgoT())
		producer := queues.NewMockQueueProducer(ctrl)
		producer.EXPECT().Enqueue(gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()
		eventsSvc := events.NewServiceHandler(suite.EventStore, worker_client.NewWorkerClient(producer, suite.Log), suite.Log)
		deviceSvc = deviceservice.NewDeviceServiceHandler(devStore, nil, fleetStore, eventsSvc, nil, "", suite.Log,
			deviceservice.WithApprovedDescriptorProvider(descriptors), deviceservice.WithRepositoryGetter(suite.Repository.GetRepository))
		fleetSvc = fleetservice.NewServiceHandler(fleetStore, nil, eventsSvc, suite.Log,
			fleetservice.WithApprovedDescriptorProvider(descriptors), fleetservice.WithRepositoryGetter(suite.Repository.GetRepository))
	})

	AfterEach(func() {
		suite.Teardown()
	})

	firmware := func() *api.FirmwareSpec {
		var spec api.FirmwareSpec
		Expect(json.Unmarshal([]byte(firmwareJSON), &spec)).To(Succeed())
		return &spec
	}
	firmwareConfig := func(spec *api.FirmwareSpec, path ...any) map[string]any {
		var current any = spec.Updater.Config
		for _, step := range path {
			switch s := step.(type) {
			case string:
				current = current.(map[string]any)[s]
			case int:
				current = current.([]any)[s]
			}
		}
		return current.(map[string]any)
	}
	deviceSpec := func(fw *api.FirmwareSpec) *api.DeviceSpec {
		var config []api.ConfigProviderSpec
		Expect(json.Unmarshal([]byte(`[{"name": "git", "gitRef": {"repository": "firmware-artifacts", "targetRevision": "main", "path": "/etc"}},
			{"name": "http", "httpRef": {"repository": "config-http", "filePath": "/etc/http.conf"}}]`), &config)).To(Succeed())
		return &api.DeviceSpec{Config: &config, Firmware: fw}
	}
	newDevice := func(name string, spec *api.DeviceSpec) api.Device {
		return api.Device{ApiVersion: "v1beta1", Kind: api.DeviceKind, Metadata: api.ObjectMeta{Name: lo.ToPtr(name)}, Spec: spec}
	}
	newFleet := func(name string, fw *api.FirmwareSpec) api.Fleet {
		fleet := api.Fleet{
			ApiVersion: "v1beta1", Kind: api.FleetKind,
			Metadata: api.ObjectMeta{Name: lo.ToPtr(name)},
			Spec:     api.FleetSpec{Selector: &api.LabelSelector{MatchLabels: &map[string]string{"site": "lab"}}},
		}
		fleet.Spec.Template.Spec = api.DeviceSpec{Firmware: fw}
		return fleet
	}
	deviceRefs := func(name string) []string {
		repos, err := devStore.GetRepositoryRefs(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		names := lo.Map(repos.Items, func(r api.Repository, _ int) string { return lo.FromPtr(r.Metadata.Name) })
		slices.Sort(names)
		return names
	}
	fleetRefs := func(name string) []string {
		repos, err := fleetStore.GetRepositoryRefs(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		return lo.Map(repos.Items, func(r api.Repository, _ int) string { return lo.FromPtr(r.Metadata.Name) })
	}
	repositoryRowExists := func(org uuid.UUID, name string) bool {
		var count int64
		Expect(suite.DB.WithContext(ctx).Table("repositories").Where("org_id = ? AND name = ?", org, name).Count(&count).Error).To(Succeed())
		return count > 0
	}
	storedDevice := func(name string) *api.Device {
		device, err := devStore.Get(ctx, orgId, name)
		Expect(err).ToNot(HaveOccurred())
		return device
	}
	failDeviceRefWrites := func(message string) *atomic.Int32 {
		var calls atomic.Int32
		callbackName := "test:fail-device-repos"
		Expect(suite.DB.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
			if tx.Statement.Table == "device_repos" {
				calls.Add(1)
				_ = tx.AddError(errors.New(message))
			}
		})).To(Succeed())
		DeferCleanup(func() {
			Expect(suite.DB.Callback().Create().Remove(callbackName)).To(Succeed())
		})
		return &calls
	}
	completeSet := []string{"config-http", "firmware-artifacts", "firmware-signatures"}

	Context("device writes", func() {
		It("When a device with valid firmware is created it should store the device and its complete reference set", func() {
			_, status := deviceSvc.CreateDevice(ctx, orgId, newDevice("device-1", deviceSpec(firmware())))

			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
			Expect(deviceRefs("device-1")).To(Equal(completeSet))
			Expect(lo.FromPtr(storedDevice("device-1").Metadata.Generation)).To(Equal(int64(1)))
			Expect(descriptors.lookups.Load()).To(Equal(int32(1)))
		})

		It("When the firmware references a repository of another organization it should store nothing", func() {
			fw := firmware()
			firmwareConfig(fw, "overlays", 0, "artifact", "authentication", "signature", "source")["repository"] = "other-signatures"

			_, status := deviceSvc.CreateDevice(ctx, orgId, newDevice("device-1", deviceSpec(fw)))

			Expect(status.Code).To(Equal(int32(http.StatusBadRequest)), status.Message)
			Expect(status.Message).To(ContainSubstring("spec.firmware.updater.config.overlays[0].artifact.authentication.signature.source.repository"))
			Expect(status.Message).To(ContainSubstring("repository not found"))
			_, err := devStore.Get(ctx, orgId, "device-1")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
			Expect(repositoryRowExists(orgId, "other-signatures")).To(BeFalse())
		})

		It("When writing the references of a new device fails it should not create the device", func() {
			calls := failDeviceRefWrites("injected join table failure")

			_, status := deviceSvc.CreateDevice(ctx, orgId, newDevice("device-1", deviceSpec(firmware())))

			Expect(status.Code).To(Equal(int32(http.StatusInternalServerError)), status.Message)
			Expect(calls.Load()).To(Equal(int32(1)))
			_, err := devStore.Get(ctx, orgId, "device-1")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("When writing the references of a replacement fails it should keep the device and its references", func() {
			_, status := deviceSvc.CreateDevice(ctx, orgId, newDevice("device-1", deviceSpec(firmware())))
			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
			before := storedDevice("device-1")
			calls := failDeviceRefWrites("injected join table failure")
			fw := firmware()
			fw.Updater.Config["overlays"] = []any{}

			_, status = deviceSvc.ReplaceDevice(ctx, orgId, "device-1", newDevice("device-1", deviceSpec(fw)), nil, true, true)

			Expect(status.Code).To(Equal(int32(http.StatusInternalServerError)), status.Message)
			Expect(calls.Load()).To(BeNumerically(">=", 1))
			after := storedDevice("device-1")
			Expect(after.Spec).To(Equal(before.Spec))
			Expect(after.Metadata.Generation).To(Equal(before.Metadata.Generation))
			Expect(after.Metadata.ResourceVersion).To(Equal(before.Metadata.ResourceVersion))
			Expect(deviceRefs("device-1")).To(Equal(completeSet))
		})

		It("When writing the references of a new device deadlocks it should roll back once without retrying", func() {
			calls := failDeviceRefWrites("ERROR: deadlock detected (SQLSTATE 40P01)")

			_, status := deviceSvc.CreateDevice(ctx, orgId, newDevice("device-1", deviceSpec(firmware())))

			Expect(status.Code).To(Equal(int32(http.StatusInternalServerError)), status.Message)
			Expect(calls.Load()).To(Equal(int32(1)))
			_, err := devStore.Get(ctx, orgId, "device-1")
			Expect(err).To(MatchError(flterrors.ErrResourceNotFound))
		})

		It("When a replacement changes the firmware it should store the spec and references together and reject an invalid replacement without changes", func() {
			_, status := deviceSvc.CreateDevice(ctx, orgId, newDevice("device-1", deviceSpec(firmware())))
			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)

			fw := firmware()
			fw.Updater.Config["overlays"] = []any{}
			_, status = deviceSvc.ReplaceDevice(ctx, orgId, "device-1", newDevice("device-1", deviceSpec(fw)), nil, true, true)
			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			Expect(deviceRefs("device-1")).To(Equal([]string{"config-http", "firmware-artifacts"}))
			replaced := storedDevice("device-1")
			Expect(lo.FromPtr(replaced.Metadata.Generation)).To(Equal(int64(2)))

			unauthorized := firmware()
			unauthorized.Updater.Version = "0.2.0"
			_, status = deviceSvc.ReplaceDevice(ctx, orgId, "device-1", newDevice("device-1", deviceSpec(unauthorized)), nil, true, true)
			Expect(status.Code).To(Equal(int32(http.StatusBadRequest)), status.Message)
			after := storedDevice("device-1")
			Expect(after.Spec).To(Equal(replaced.Spec))
			Expect(after.Metadata.Generation).To(Equal(replaced.Metadata.Generation))
			Expect(after.Metadata.ResourceVersion).To(Equal(replaced.Metadata.ResourceVersion))
			Expect(deviceRefs("device-1")).To(Equal([]string{"config-http", "firmware-artifacts"}))
		})

		It("When a replacement omits the firmware it should keep it and leave the generation and references unchanged", func() {
			_, status := deviceSvc.CreateDevice(ctx, orgId, newDevice("device-1", deviceSpec(firmware())))
			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
			created := storedDevice("device-1")

			_, status = deviceSvc.ReplaceDevice(ctx, orgId, "device-1", newDevice("device-1", deviceSpec(nil)), nil, true, true)

			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			after := storedDevice("device-1")
			Expect(after.Spec.Firmware).To(Equal(created.Spec.Firmware))
			Expect(after.Metadata.Generation).To(Equal(created.Metadata.Generation))
			Expect(deviceRefs("device-1")).To(Equal(completeSet))
			Expect(descriptors.lookups.Load()).To(Equal(int32(1)))
		})

		It("When a patch removes the firmware it should withdraw it and keep the shared configuration reference", func() {
			_, status := deviceSvc.CreateDevice(ctx, orgId, newDevice("device-1", deviceSpec(firmware())))
			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)

			_, status = deviceSvc.PatchDevice(ctx, orgId, "device-1", api.PatchRequest{{Op: "remove", Path: "/spec/firmware"}}, true, true)

			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			Expect(storedDevice("device-1").Spec.Firmware).To(BeNil())
			Expect(deviceRefs("device-1")).To(Equal([]string{"config-http", "firmware-artifacts"}))
		})

		It("When the device changes between validation and persistence it should validate the fresh device again", func() {
			_, status := deviceSvc.CreateDevice(ctx, orgId, newDevice("device-1", deviceSpec(nil)))
			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
			concurrentStore := devicestore.NewDeviceStore(suite.DB, suite.Log)
			var interleaved atomic.Bool
			devStore.SetIntegrationTestCreateOrUpdateCallback(func() {
				if interleaved.CompareAndSwap(false, true) {
					Expect(concurrentStore.UpdateAnnotations(ctx, orgId, "device-1", map[string]string{"concurrent": "write"}, nil)).To(Succeed())
				}
			})
			DeferCleanup(func() { devStore.SetIntegrationTestCreateOrUpdateCallback(func() {}) })

			fwValue := map[string]any{}
			Expect(json.Unmarshal([]byte(firmwareJSON), &fwValue)).To(Succeed())
			_, status = deviceSvc.PatchDevice(ctx, orgId, "device-1", api.PatchRequest{{Op: "add", Path: "/spec/firmware", Value: fwValue}}, true, true)

			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			// The early check, the first attempt and the attempt after the conflict.
			Expect(descriptors.lookups.Load()).To(Equal(int32(3)))
			after := storedDevice("device-1")
			Expect(after.Spec.Firmware).ToNot(BeNil())
			Expect(lo.FromPtr(after.Metadata.Annotations)).To(HaveKeyWithValue("concurrent", "write"))
			Expect(deviceRefs("device-1")).To(Equal(completeSet))
		})
	})

	Context("fleet writes", func() {
		It("When a fleet with literal firmware is created it should validate it now and leave fleet references to fleet validation", func() {
			_, status := fleetSvc.CreateFleet(ctx, orgId, newFleet("fleet-1", firmware()))

			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
			Expect(descriptors.lookups.Load()).To(Equal(int32(1)))
			Expect(fleetRefs("fleet-1")).To(BeEmpty())
		})

		It("When a fleet with a templated updater identity is created it should store it without release lookups", func() {
			fw := firmware()
			fw.Updater.Version = "{{ .metadata.labels.updaterVersion }}"

			_, status := fleetSvc.CreateFleet(ctx, orgId, newFleet("fleet-1", fw))

			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
			Expect(descriptors.lookups.Load()).To(BeZero())
		})

		It("When a fleet replacement has a missing literal repository beside a templated value it should leave the fleet unchanged", func() {
			_, status := fleetSvc.CreateFleet(ctx, orgId, newFleet("fleet-1", nil))
			Expect(status.Code).To(Equal(int32(http.StatusCreated)), status.Message)
			before, err := fleetStore.Get(ctx, orgId, "fleet-1")
			Expect(err).ToNot(HaveOccurred())
			fw := firmware()
			firmwareConfig(fw, "resources", 0)["targetGuid"] = "{{ .metadata.labels.guid }}"
			firmwareConfig(fw, "releases", 0, "artifact", "source")["repository"] = "missing-artifacts"

			_, status = fleetSvc.ReplaceFleet(ctx, orgId, "fleet-1", newFleet("fleet-1", fw), true)

			Expect(status.Code).To(Equal(int32(http.StatusBadRequest)), status.Message)
			Expect(status.Message).To(ContainSubstring("releases[0].artifact.source.repository"))
			after, err := fleetStore.Get(ctx, orgId, "fleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(after.Spec).To(Equal(before.Spec))
			Expect(after.Metadata.Generation).To(Equal(before.Metadata.Generation))
			Expect(repositoryRowExists(orgId, "missing-artifacts")).To(BeFalse())
		})
	})

	Context("raw HTTP requests", func() {
		var apiServer *httptest.Server

		BeforeEach(func() {
			swagger, err := api.GetSpec()
			Expect(err).ToNot(HaveOccurred())
			validator := oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapimiddleware.Options{
				ErrorHandler:          apiserver.OapiErrorHandlerForVersion(versioning.V1Beta1),
				MultiErrorHandler:     apiserver.OapiMultiErrorHandler,
				SilenceServersWarning: true,
			})
			handler := transportv1beta1.NewTransportHandler(nil, nil, deviceSvc, nil, nil, nil, nil, nil, fleetSvc, nil, nil, nil, nil,
				convertv1beta1.NewConverter(), nil, nil, nil, nil)
			identity := ctx.Value(consts.MappedIdentityCtxKey)
			router := chi.NewRouter()
			router.Use(func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requestCtx := context.WithValue(util.WithOrganizationID(r.Context(), orgId), consts.MappedIdentityCtxKey, identity)
					next.ServeHTTP(w, r.WithContext(requestCtx))
				})
			})
			router.Route(server.ServerUrlApiv1, func(r chi.Router) {
				r.Use(validator)
				server.HandlerWithOptions(handler, server.ChiServerOptions{
					BaseRouter: r,
					ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
						apiserver.OapiErrorHandlerForVersion(versioning.V1Beta1)(w, err.Error(), http.StatusBadRequest)
					},
				})
			})
			apiServer = httptest.NewServer(router)
			DeferCleanup(apiServer.Close)
		})

		send := func(method, path, body string) (int, string) {
			contentType := "application/json"
			if method == http.MethodPatch {
				contentType = "application/json-patch+json"
			}
			req, err := http.NewRequestWithContext(ctx, method, apiServer.URL+server.ServerUrlApiv1+path, strings.NewReader(body))
			Expect(err).ToNot(HaveOccurred())
			req.Header.Set("Content-Type", contentType)
			resp, err := http.DefaultClient.Do(req)
			Expect(err).ToNot(HaveOccurred())
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			Expect(err).ToNot(HaveOccurred())
			return resp.StatusCode, string(raw)
		}
		deviceBody := func(name, firmwareDocument string) string {
			return `{"apiVersion": "v1beta1", "kind": "Device", "metadata": {"name": "` + name + `"},
				"spec": {"config": [{"name": "http", "httpRef": {"repository": "config-http", "filePath": "/etc/http.conf"}}],
				"firmware": ` + firmwareDocument + `}}`
		}
		fleetBody := func(name, firmwareDocument string) string {
			return `{"apiVersion": "v1beta1", "kind": "Fleet", "metadata": {"name": "` + name + `"},
				"spec": {"selector": {"matchLabels": {"site": "lab"}}, "template": {"spec": {"firmware": ` + firmwareDocument + `}}}}`
		}
		firmwareDocument := func(mutate func(fw *api.FirmwareSpec)) string {
			fw := firmware()
			if mutate != nil {
				mutate(fw)
			}
			raw, err := json.Marshal(fw)
			Expect(err).ToNot(HaveOccurred())
			return string(raw)
		}
		deviceExists := func(name string) bool {
			_, err := devStore.Get(ctx, orgId, name)
			if errors.Is(err, flterrors.ErrResourceNotFound) {
				return false
			}
			Expect(err).ToNot(HaveOccurred())
			return true
		}
		fleetExists := func(name string) bool {
			_, err := fleetStore.Get(ctx, orgId, name)
			if errors.Is(err, flterrors.ErrResourceNotFound) {
				return false
			}
			Expect(err).ToNot(HaveOccurred())
			return true
		}

		It("When a device body has an unknown firmware field it should reject it before the service runs", func() {
			code, body := send(http.MethodPost, "/devices", deviceBody("device-1", strings.Replace(firmwareDocument(nil), `"profile"`, `"flashNow": true, "profile"`, 1)))

			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring("flashNow"))
			Expect(deviceExists("device-1")).To(BeFalse())
			Expect(descriptors.lookups.Load()).To(BeZero())
		})

		It("When a device body sets the firmware to null it should reject it", func() {
			code, body := send(http.MethodPost, "/devices", deviceBody("device-1", "null"))

			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(deviceExists("device-1")).To(BeFalse())
		})

		It("When a device body references a missing repository it should reject it with the field path and store nothing", func() {
			code, body := send(http.MethodPost, "/devices", deviceBody("device-1", firmwareDocument(func(fw *api.FirmwareSpec) {
				firmwareConfig(fw, "releases", 0, "artifact", "source")["repository"] = "missing-artifacts"
			})))

			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring("spec.firmware.updater.config.releases[0].artifact.source.repository"))
			Expect(deviceExists("device-1")).To(BeFalse())
			Expect(repositoryRowExists(orgId, "missing-artifacts")).To(BeFalse())
		})

		It("When firmware is created, patched and replaced over HTTP it should enforce the raw firmware fields and keep omitted firmware", func() {
			code, body := send(http.MethodPost, "/devices", deviceBody("device-1", firmwareDocument(nil)))
			Expect(code).To(Equal(http.StatusCreated), body)
			created := storedDevice("device-1")
			Expect(created.Spec.Firmware).ToNot(BeNil())

			By("rejecting a patch that adds a field differing from a firmware field only in case")
			code, body = send(http.MethodPatch, "/devices/device-1", `[{"op": "add", "path": "/spec/firmware/updater/Version", "value": "0.2.0"}]`)
			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring(`property \"Version\" is unsupported`))
			Expect(storedDevice("device-1").Spec).To(Equal(created.Spec))

			By("keeping an unknown updater configuration field for the release schema to reject")
			code, body = send(http.MethodPatch, "/devices/device-1", `[{"op": "add", "path": "/spec/firmware/updater/config/flashNow", "value": true}]`)
			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring("flashNow"))
			Expect(storedDevice("device-1").Spec).To(Equal(created.Spec))

			By("rejecting a patch that sets the firmware to null and keeping the firmware")
			code, body = send(http.MethodPatch, "/devices/device-1", `[{"op": "replace", "path": "/spec/firmware", "value": null}]`)
			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring("not nullable"))
			Expect(storedDevice("device-1").Spec.Firmware).To(Equal(created.Spec.Firmware))

			By("keeping the firmware when a replacement omits it")
			code, body = send(http.MethodPut, "/devices/device-1", `{"apiVersion": "v1beta1", "kind": "Device", "metadata": {"name": "device-1"},
				"spec": {"config": [{"name": "http", "httpRef": {"repository": "config-http", "filePath": "/etc/http.conf"}}]}}`)
			Expect(code).To(Equal(http.StatusOK), body)
			Expect(storedDevice("device-1").Spec.Firmware).To(Equal(created.Spec.Firmware))

			By("withdrawing the firmware on an explicit removal")
			code, body = send(http.MethodPatch, "/devices/device-1", `[{"op": "remove", "path": "/spec/firmware"}]`)
			Expect(code).To(Equal(http.StatusOK), body)
			Expect(storedDevice("device-1").Spec.Firmware).To(BeNil())
			Expect(deviceRefs("device-1")).To(Equal([]string{"config-http"}))
		})

		It("When a fleet body templates only a designated GUID it should accept it, and reject an unknown field or invalid literal beside it", func() {
			templatedGUID := func(fw *api.FirmwareSpec) {
				firmwareConfig(fw, "resources", 0)["targetGuid"] = "{{ .metadata.labels.guid }}"
			}

			code, body := send(http.MethodPost, "/fleets", fleetBody("fleet-unknown", firmwareDocument(func(fw *api.FirmwareSpec) {
				templatedGUID(fw)
				firmwareConfig(fw, "resources", 0)["flash"] = true
			})))
			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(fleetExists("fleet-unknown")).To(BeFalse())

			code, body = send(http.MethodPost, "/fleets", fleetBody("fleet-literal", firmwareDocument(func(fw *api.FirmwareSpec) {
				templatedGUID(fw)
				firmwareConfig(fw, "resources", 1)["targetGuid"] = "not-a-guid"
			})))
			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring("resources[1].targetGuid"))
			Expect(fleetExists("fleet-literal")).To(BeFalse())

			code, body = send(http.MethodPost, "/fleets", fleetBody("fleet-1", firmwareDocument(templatedGUID)))
			Expect(code).To(Equal(http.StatusCreated), body)
			stored, err := fleetStore.Get(ctx, orgId, "fleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(firmwareConfig(stored.Spec.Template.Spec.Firmware, "resources", 0)["targetGuid"]).To(Equal("{{ .metadata.labels.guid }}"))
			Expect(fleetRefs("fleet-1")).To(BeEmpty())
		})

		It("When a fleet body templates the updater identity it should accept it without release lookups", func() {
			code, body := send(http.MethodPost, "/fleets", fleetBody("fleet-1", firmwareDocument(func(fw *api.FirmwareSpec) {
				fw.Updater.Name = "{{ .metadata.labels.updater }}"
			})))

			Expect(code).To(Equal(http.StatusCreated), body)
			Expect(descriptors.lookups.Load()).To(BeZero())
		})

		decodedJSON := func(document string) any {
			var value any
			Expect(json.Unmarshal([]byte(document), &value)).To(Succeed())
			return value
		}
		// withSchemaDefaults decodes a firmware document and adds the API schema
		// defaults that the request validator applies before storing it.
		withSchemaDefaults := func(document string) any {
			firmware := decodedJSON(document).(map[string]any)
			if _, ok := firmware["retryGeneration"]; !ok {
				firmware["retryGeneration"] = float64(0)
			}
			for _, entry := range firmware["components"].([]any) {
				version := entry.(map[string]any)["version"].(map[string]any)
				if _, ok := version["latest"]; !ok {
					version["latest"] = false
				}
			}
			return firmware
		}
		read := func(path string) map[string]any {
			code, body := send(http.MethodGet, path, "")
			Expect(code).To(Equal(http.StatusOK), body)
			var value map[string]any
			Expect(json.Unmarshal([]byte(body), &value)).To(Succeed())
			return value
		}
		member := func(value any, path ...string) any {
			for _, key := range path {
				object, ok := value.(map[string]any)
				if !ok {
					return nil
				}
				value = object[key]
			}
			return value
		}
		editedDocument := func(document string, edit func(firmware map[string]any)) string {
			value := decodedJSON(document).(map[string]any)
			edit(value)
			raw, err := json.Marshal(value)
			Expect(err).ToNot(HaveOccurred())
			return string(raw)
		}
		component := func(firmware map[string]any, index int) map[string]any {
			return firmware["components"].([]any)[index].(map[string]any)
		}
		updaterConfig := func(firmware map[string]any) map[string]any {
			return firmware["updater"].(map[string]any)["config"].(map[string]any)
		}
		configResource := func(firmware map[string]any, index int) map[string]any {
			return updaterConfig(firmware)["resources"].([]any)[index].(map[string]any)
		}
		fleetRefsOf := func(name string) []string {
			refs := fleetRefs(name)
			slices.Sort(refs)
			return refs
		}

		It("When a device with every firmware field is created, replaced and patched over HTTP it should read back every field exactly", func() {
			code, body := send(http.MethodPost, "/devices", deviceBody("device-full", fullFirmwareJSON))
			Expect(code).To(Equal(http.StatusCreated), body)
			Expect(member(read("/devices/device-full"), "spec", "firmware")).To(Equal(withSchemaDefaults(fullFirmwareJSON)))

			replaced := editedDocument(fullFirmwareJSON, func(fw map[string]any) {
				fw["retryGeneration"] = 4
				component(fw, 1)["version"] = map[string]any{"exact": "1.11.0-beta.2"}
				component(fw, 2)["artifactDigest"] = "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
			})
			code, body = send(http.MethodPut, "/devices/device-full", deviceBody("device-full", replaced))
			Expect(code).To(Equal(http.StatusOK), body)
			Expect(member(read("/devices/device-full"), "spec", "firmware")).To(Equal(withSchemaDefaults(replaced)))

			code, body = send(http.MethodPatch, "/devices/device-full", `[
				{"op": "replace", "path": "/spec/firmware/components/0/version/exact", "value": "36.4.5"},
				{"op": "add", "path": "/spec/firmware/overlays/0/compatibleFirmware/-", "value": {"component": "uefi", "minimum": "2024.05"}}]`)
			Expect(code).To(Equal(http.StatusOK), body)
			patched := editedDocument(replaced, func(fw map[string]any) {
				component(fw, 0)["version"] = map[string]any{"exact": "36.4.5"}
				overlay := fw["overlays"].([]any)[0].(map[string]any)
				overlay["compatibleFirmware"] = append(overlay["compatibleFirmware"].([]any), map[string]any{"component": "uefi", "minimum": "2024.05"})
			})
			Expect(member(read("/devices/device-full"), "spec", "firmware")).To(Equal(withSchemaDefaults(patched)))
			Expect(deviceRefs("device-full")).To(Equal(completeSet))

			code, body = send(http.MethodPost, "/devices", `{"apiVersion": "v1beta1", "kind": "Device", "metadata": {"name": "device-plain"}, "spec": {}}`)
			Expect(code).To(Equal(http.StatusCreated), body)
			Expect(read("/devices/device-plain")["spec"]).ToNot(HaveKey("firmware"))
		})

		It("When a fleet with every firmware field is created, replaced and patched over HTTP it should read back every field exactly", func() {
			created := editedDocument(fullFirmwareJSON, func(fw map[string]any) {
				configResource(fw, 1)["targetGuid"] = "{{ .metadata.labels.cameraGuid }}"
			})
			code, body := send(http.MethodPost, "/fleets", fleetBody("fleet-full", created))
			Expect(code).To(Equal(http.StatusCreated), body)
			Expect(member(read("/fleets/fleet-full"), "spec", "template", "spec", "firmware")).To(Equal(withSchemaDefaults(created)))

			replaced := editedDocument(created, func(fw map[string]any) {
				fw["profile"] = "orin-example-profile-b"
				component(fw, 2)["version"] = map[string]any{"maximum": "2025.01"}
			})
			code, body = send(http.MethodPut, "/fleets/fleet-full", fleetBody("fleet-full", replaced))
			Expect(code).To(Equal(http.StatusOK), body)
			Expect(member(read("/fleets/fleet-full"), "spec", "template", "spec", "firmware")).To(Equal(withSchemaDefaults(replaced)))

			code, body = send(http.MethodPatch, "/fleets/fleet-full", `[{"op": "replace", "path": "/spec/template/spec/firmware/retryGeneration", "value": 5}]`)
			Expect(code).To(Equal(http.StatusOK), body)
			patched := editedDocument(replaced, func(fw map[string]any) { fw["retryGeneration"] = 5 })
			Expect(member(read("/fleets/fleet-full"), "spec", "template", "spec", "firmware")).To(Equal(withSchemaDefaults(patched)))
		})

		It("When a device body has unknown or case-variant keys in nested firmware objects it should reject each and store nothing", func() {
			for _, tc := range []struct {
				key  string
				edit func(fw map[string]any)
			}{
				{key: "flash", edit: func(fw map[string]any) { fw["updater"].(map[string]any)["flash"] = true }},
				{key: "Exact", edit: func(fw map[string]any) { component(fw, 0)["version"] = map[string]any{"Exact": "36.4.4"} }},
				{key: "rollback", edit: func(fw map[string]any) { component(fw, 1)["rollback"] = true }},
				{key: "required", edit: func(fw map[string]any) {
					fw["overlays"].([]any)[0].(map[string]any)["compatibleFirmware"].([]any)[0].(map[string]any)["required"] = true
				}},
			} {
				code, body := send(http.MethodPost, "/devices", deviceBody("device-nested", editedDocument(fullFirmwareJSON, tc.edit)))
				Expect(code).To(Equal(http.StatusBadRequest), "%s: %s", tc.key, body)
				Expect(body).To(ContainSubstring(tc.key))
				Expect(deviceExists("device-nested")).To(BeFalse())
			}
			Expect(descriptors.lookups.Load()).To(BeZero())
		})

		It("When a device body has an invalid version constraint or digest it should reject it with the field path and store nothing", func() {
			for _, tc := range []struct {
				path string
				edit func(fw map[string]any)
			}{
				{path: "spec.firmware.components[1].version", edit: func(fw map[string]any) { component(fw, 1)["version"] = map[string]any{} }},
				{path: "spec.firmware.components[1].version", edit: func(fw map[string]any) { component(fw, 1)["version"] = map[string]any{"latest": false} }},
				{path: "components", edit: func(fw map[string]any) { component(fw, 1)["version"] = map[string]any{"minimum": ""} }},
				{path: "artifactDigest", edit: func(fw map[string]any) { component(fw, 0)["artifactDigest"] = "sha256:XYZ" }},
			} {
				code, body := send(http.MethodPost, "/devices", deviceBody("device-invalid", editedDocument(fullFirmwareJSON, tc.edit)))
				Expect(code).To(Equal(http.StatusBadRequest), "%s: %s", tc.path, body)
				Expect(body).To(ContainSubstring(tc.path))
				Expect(deviceExists("device-invalid")).To(BeFalse())
			}
		})

		It("When a fleet body has a literal updater identity it should reject an unknown configuration field, a forbidden template and a missing repository now", func() {
			for _, tc := range []struct {
				name     string
				fragment string
				edit     func(fw map[string]any)
			}{
				{name: "fleet-unknown", fragment: "flash", edit: func(fw map[string]any) { configResource(fw, 0)["flash"] = true }},
				{name: "fleet-template", fragment: "resources[0].component", edit: func(fw map[string]any) { configResource(fw, 0)["component"] = "{{ .metadata.labels.component }}" }},
				{name: "fleet-missing", fragment: "releases[0].artifact.source.repository", edit: func(fw map[string]any) {
					updaterConfig(fw)["releases"].([]any)[0].(map[string]any)["artifact"].(map[string]any)["source"].(map[string]any)["repository"] = "missing-artifacts"
				}},
			} {
				code, body := send(http.MethodPost, "/fleets", fleetBody(tc.name, editedDocument(firmwareJSON, tc.edit)))
				Expect(code).To(Equal(http.StatusBadRequest), "%s: %s", tc.name, body)
				Expect(body).To(ContainSubstring(tc.fragment))
				Expect(fleetExists(tc.name)).To(BeFalse())
			}
			Expect(repositoryRowExists(orgId, "missing-artifacts")).To(BeFalse())
		})

		It("When a fleet body templates only part of the updater identity it should skip the release lookup and still apply every common check now", func() {
			templatedVersion := func(fw map[string]any) {
				fw["updater"].(map[string]any)["version"] = "{{ .metadata.labels.updaterVersion }}"
			}

			code, body := send(http.MethodPost, "/fleets", fleetBody("fleet-digest", editedDocument(fullFirmwareJSON, func(fw map[string]any) {
				templatedVersion(fw)
				component(fw, 0)["artifactDigest"] = "sha256:XYZ"
			})))
			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring("artifactDigest"))
			Expect(fleetExists("fleet-digest")).To(BeFalse())

			code, body = send(http.MethodPost, "/fleets", fleetBody("fleet-templated-digest", editedDocument(fullFirmwareJSON, func(fw map[string]any) {
				templatedVersion(fw)
				component(fw, 0)["artifactDigest"] = "{{ .metadata.labels.digest }}"
			})))
			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(fleetExists("fleet-templated-digest")).To(BeFalse())

			accepted := editedDocument(fullFirmwareJSON, templatedVersion)
			code, body = send(http.MethodPost, "/fleets", fleetBody("fleet-1", accepted))
			Expect(code).To(Equal(http.StatusCreated), body)
			Expect(member(read("/fleets/fleet-1"), "spec", "template", "spec", "firmware")).To(Equal(withSchemaDefaults(accepted)))
			Expect(descriptors.lookups.Load()).To(BeZero())
		})

		It("When a fleet body templates a designated repository name it should defer it, but reject a missing literal repository beside it", func() {
			templatedArtifacts := func(fw map[string]any) {
				updaterConfig(fw)["releases"].([]any)[0].(map[string]any)["artifact"].(map[string]any)["source"].(map[string]any)["repository"] = "{{ .metadata.labels.artifacts }}"
			}
			code, body := send(http.MethodPost, "/fleets", fleetBody("fleet-missing", editedDocument(firmwareJSON, func(fw map[string]any) {
				templatedArtifacts(fw)
				updaterConfig(fw)["overlays"].([]any)[0].(map[string]any)["artifact"].(map[string]any)["authentication"].(map[string]any)["signature"].(map[string]any)["source"].(map[string]any)["repository"] = "missing-signatures"
			})))
			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring("overlays[0].artifact.authentication.signature.source.repository"))
			Expect(fleetExists("fleet-missing")).To(BeFalse())

			accepted := editedDocument(firmwareJSON, templatedArtifacts)
			code, body = send(http.MethodPost, "/fleets", fleetBody("fleet-1", accepted))
			Expect(code).To(Equal(http.StatusCreated), body)
			Expect(member(read("/fleets/fleet-1"), "spec", "template", "spec", "firmware")).To(Equal(withSchemaDefaults(accepted)))
			Expect(repositoryRowExists(orgId, "{{ .metadata.labels.artifacts }}")).To(BeFalse())
			Expect(repositoryRowExists(orgId, "missing-signatures")).To(BeFalse())
		})

		It("When a fleet with references is replaced or patched with a templated GUID beside invalid data it should leave the fleet and its references unchanged", func() {
			code, body := send(http.MethodPost, "/fleets", fleetBody("fleet-1", firmwareJSON))
			Expect(code).To(Equal(http.StatusCreated), body)
			stored, err := fleetStore.Get(ctx, orgId, "fleet-1")
			Expect(err).ToNot(HaveOccurred())
			outcome, status := fleetSvc.ReplaceFleetRepositoryRefsIfCurrent(ctx, orgId, "fleet-1", lo.FromPtr(stored.Metadata.Generation), &stored.Spec.Template.Spec)
			Expect(status.Code).To(Equal(int32(http.StatusOK)), status.Message)
			Expect(outcome).To(Equal(common.RepositoryRefsReplaced))
			Expect(fleetRefsOf("fleet-1")).To(Equal([]string{"firmware-artifacts", "firmware-signatures"}))
			expectUnchanged := func() {
				after, err := fleetStore.Get(ctx, orgId, "fleet-1")
				Expect(err).ToNot(HaveOccurred())
				Expect(after.Spec).To(Equal(stored.Spec))
				Expect(after.Metadata.Generation).To(Equal(stored.Metadata.Generation))
				Expect(after.Metadata.ResourceVersion).To(Equal(stored.Metadata.ResourceVersion))
				Expect(fleetRefsOf("fleet-1")).To(Equal([]string{"firmware-artifacts", "firmware-signatures"}))
			}

			code, body = send(http.MethodPut, "/fleets/fleet-1", fleetBody("fleet-1", editedDocument(firmwareJSON, func(fw map[string]any) {
				configResource(fw, 0)["targetGuid"] = "{{ .metadata.labels.guid }}"
				configResource(fw, 0)["flash"] = true
			})))
			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring("flash"))
			expectUnchanged()

			code, body = send(http.MethodPatch, "/fleets/fleet-1", `[
				{"op": "replace", "path": "/spec/template/spec/firmware/updater/config/resources/0/targetGuid", "value": "{{ .metadata.labels.guid }}"},
				{"op": "replace", "path": "/spec/template/spec/firmware/updater/config/resources/1/targetGuid", "value": "not-a-guid"}]`)
			Expect(code).To(Equal(http.StatusBadRequest), body)
			Expect(body).To(ContainSubstring("resources[1].targetGuid"))
			expectUnchanged()

			code, body = send(http.MethodPatch, "/fleets/fleet-1", `[
				{"op": "replace", "path": "/spec/template/spec/firmware/updater/config/resources/0/targetGuid", "value": "{{ .metadata.labels.guid }}"}]`)
			Expect(code).To(Equal(http.StatusOK), body)
			after, err := fleetStore.Get(ctx, orgId, "fleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(firmwareConfig(after.Spec.Template.Spec.Firmware, "resources", 0)["targetGuid"]).To(Equal("{{ .metadata.labels.guid }}"))
			Expect(fleetRefsOf("fleet-1")).To(Equal([]string{"firmware-artifacts", "firmware-signatures"}), "admission leaves fleet references to fleet validation")
		})

		It("When a fleet replacement sets the template firmware to null it should reject it", func() {
			code, body := send(http.MethodPost, "/fleets", fleetBody("fleet-1", firmwareDocument(nil)))
			Expect(code).To(Equal(http.StatusCreated), body)

			code, body = send(http.MethodPut, "/fleets/fleet-1", fleetBody("fleet-1", "null"))

			Expect(code).To(Equal(http.StatusBadRequest), body)
			stored, err := fleetStore.Get(ctx, orgId, "fleet-1")
			Expect(err).ToNot(HaveOccurred())
			Expect(stored.Spec.Template.Spec.Firmware).ToNot(BeNil())
		})
	})
})
