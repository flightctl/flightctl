package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// admissionConfigSchema is a closed updater configuration schema for the
// test-only release below.
const admissionConfigSchema = `{
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

const (
	admissionGUID      = "00000000-0000-4000-8000-000000000001"
	admissionOtherGUID = "00000000-0000-4000-8000-000000000002"
	guidTemplate       = "{{ .metadata.labels.guid }}"
	releaseLookup      = "nvidia-orin/0.1.0"
)

const admissionFirmwareJSON = `{
  "profile": "orin-example-profile",
  "updater": {"name": "nvidia-orin", "version": "0.1.0", "config": {
    "resources": [{"component": "system", "targetGuid": "` + admissionGUID + `"},
                  {"component": "camera", "targetGuid": "` + admissionOtherGUID + `"}],
    "releases": [{"artifact": {"source": {"repository": "firmware-artifacts"}}}],
    "overlays": [{"artifact": {"authentication": {"signature": {"source": {"repository": "firmware-signatures"}}}}}]
  }},
  "components": [{"name": "system", "version": {"latest": true}}],
  "overlays": []
}`

func admissionDescriptor() *common.ApprovedDescriptor {
	return &common.ApprovedDescriptor{
		Name: "nvidia-orin", Version: "0.1.0", Approved: true,
		ConfigSchema: json.RawMessage(admissionConfigSchema),
		ReferencePaths: []common.FirmwareReferencePath{
			{Path: "releases[].artifact.source.repository", Kind: common.FirmwareReferenceArtifact},
			{Path: "overlays[].artifact.authentication.signature.source.repository", Kind: common.FirmwareReferenceSignature},
		},
		GUIDPaths: []string{"resources[].targetGuid"},
		TemplatablePaths: []string{
			"resources[].targetGuid",
			"releases[].artifact.source.repository",
			"overlays[].artifact.authentication.signature.source.repository",
		},
	}
}

// recordingDescriptors is a test stub of the approved descriptor provider
// that records every release lookup. After revokeAfter lookups, it reports
// the release as no longer authorized.
type recordingDescriptors struct {
	releases    map[string]*common.ApprovedDescriptor
	err         error
	revokeAfter int
	lookups     []string
}

func newRecordingDescriptors(descriptors ...*common.ApprovedDescriptor) *recordingDescriptors {
	p := &recordingDescriptors{releases: map[string]*common.ApprovedDescriptor{}}
	for _, d := range descriptors {
		p.releases[d.Name+"/"+d.Version] = d
	}
	return p
}

func (p *recordingDescriptors) LookupRelease(_ context.Context, name, version string) (*common.ApprovedDescriptor, error) {
	p.lookups = append(p.lookups, name+"/"+version)
	if p.err != nil {
		return nil, p.err
	}
	descriptor := p.releases[name+"/"+version]
	if descriptor != nil && p.revokeAfter > 0 && len(p.lookups) > p.revokeAfter {
		revoked := *descriptor
		revoked.Approved = false
		return &revoked, nil
	}
	return descriptor, nil
}

func (p *recordingDescriptors) LookupSchemaByDigest(context.Context, string, string) (*common.RetainedSchemaEvidence, error) {
	return nil, errors.New("retained schema evidence is not used by admission")
}

// tenantRepositories serves Repository resources per organization, like the
// repository service, and records every lookup.
type tenantRepositories struct {
	byOrg map[uuid.UUID]map[string]*domain.Repository
	gets  []string
}

func (r *tenantRepositories) add(t *testing.T, orgId uuid.UUID, names ...string) {
	t.Helper()
	if r.byOrg[orgId] == nil {
		r.byOrg[orgId] = map[string]*domain.Repository{}
	}
	for _, name := range names {
		var spec domain.RepositorySpec
		require.NoError(t, spec.FromHttpRepoSpec(domain.HttpRepoSpec{Type: domain.HttpRepoSpecTypeHttp, Url: "https://artifacts.example.com/" + name}))
		r.byOrg[orgId][name] = &domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}, Spec: spec}
	}
}

func (r *tenantRepositories) GetRepository(_ context.Context, orgId uuid.UUID, name string) (*domain.Repository, domain.Status) {
	r.gets = append(r.gets, name)
	repository, ok := r.byOrg[orgId][name]
	if !ok {
		return nil, domain.StatusResourceNotFound(domain.RepositoryKind, name)
	}
	return repository, domain.StatusOK()
}

func admissionFirmware(t *testing.T) *domain.FirmwareSpec {
	t.Helper()
	var firmware domain.FirmwareSpec
	require.NoError(t, json.Unmarshal([]byte(admissionFirmwareJSON), &firmware))
	return &firmware
}

func admissionConfig(firmware *domain.FirmwareSpec, path ...any) map[string]any {
	var current any = firmware.Updater.Config
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

func admissionFleet(name string, firmware *domain.FirmwareSpec) domain.Fleet {
	fleet := domain.Fleet{
		ApiVersion: "v1beta1",
		Kind:       "Fleet",
		Metadata:   domain.ObjectMeta{Name: lo.ToPtr(name), Labels: &map[string]string{"site": "lab"}},
		Spec:       domain.FleetSpec{Selector: &domain.LabelSelector{MatchLabels: &map[string]string{"site": "lab"}}},
	}
	fleet.Spec.Template.Spec = domain.DeviceSpec{Os: &domain.DeviceOsSpec{Image: "quay.io/example/os:1"}, Firmware: firmware}
	return fleet
}

type admissionFixture struct {
	svc         *ServiceHandler
	store       *fakeFleetStore
	descriptors *recordingDescriptors
	repos       *tenantRepositories
	orgId       uuid.UUID
}

func newAdmissionFixture(t *testing.T, opts ...func(t *testing.T, f *admissionFixture)) *admissionFixture {
	f := &admissionFixture{
		store:       newFakeFleetStore(),
		descriptors: newRecordingDescriptors(admissionDescriptor()),
		repos:       &tenantRepositories{byOrg: map[uuid.UUID]map[string]*domain.Repository{}},
		orgId:       uuid.New(),
	}
	f.repos.add(t, f.orgId, "firmware-artifacts", "firmware-signatures")
	for _, opt := range opts {
		opt(t, f)
	}
	var handlerOpts []HandlerOption
	if f.descriptors != nil {
		handlerOpts = append(handlerOpts, WithApprovedDescriptorProvider(f.descriptors))
	}
	if f.repos != nil {
		handlerOpts = append(handlerOpts, WithRepositoryGetter(f.repos.GetRepository))
	}
	f.svc = NewServiceHandler(f.store, nil, &fakeEventsService{}, logrus.New(), handlerOpts...)
	return f
}

func (f *admissionFixture) storeFleet(t *testing.T, fleet domain.Fleet) *domain.Fleet {
	t.Helper()
	_, err := f.store.Create(context.Background(), f.orgId, &fleet)
	require.NoError(t, err)
	return deepCopyFleet(f.store.fleets[*fleet.Metadata.Name])
}

func (f *admissionFixture) resetRecords() {
	f.descriptors.lookups = nil
	f.repos.gets = nil
}

func TestCreateFleetFirmwareAdmission(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		fixture     func(t *testing.T, f *admissionFixture)
		mutate      func(firmware *domain.FirmwareSpec)
		noFirmware  bool
		wantCode    int32
		wantErr     []string
		wantLookups []string
		wantGets    []string
	}{
		{
			name:       "When the template has no firmware it should create the fleet without release or repository lookups",
			noFirmware: true,
			wantCode:   http.StatusCreated,
		},
		{
			name:        "When the firmware is all literal and valid it should authorize the release and check every repository now",
			wantCode:    http.StatusCreated,
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts", "firmware-signatures"},
		},
		{
			name:     "When approved releases are unavailable it should reject literal firmware",
			fixture:  func(t *testing.T, f *admissionFixture) { f.descriptors = nil },
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"approved updater releases are unavailable"},
		},
		{
			name: "When the literal release is not authorized it should reject it",
			fixture: func(t *testing.T, f *admissionFixture) {
				descriptor := admissionDescriptor()
				descriptor.Approved = false
				f.descriptors = newRecordingDescriptors(descriptor)
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"is not authorized"},
			wantLookups: []string{releaseLookup},
		},
		{
			name:        "When the literal configuration has an unknown nested field it should reject it",
			mutate:      func(firmware *domain.FirmwareSpec) { admissionConfig(firmware, "resources", 0)["flash"] = true },
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"spec.firmware.updater.config"},
			wantLookups: []string{releaseLookup},
		},
		{
			name: "When a literal target GUID is invalid it should reject it",
			mutate: func(firmware *domain.FirmwareSpec) {
				admissionConfig(firmware, "resources", 0)["targetGuid"] = "not-a-guid"
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"resources[0].targetGuid"},
			wantLookups: []string{releaseLookup},
		},
		{
			name: "When a literal artifact repository does not exist it should reject it",
			fixture: func(t *testing.T, f *admissionFixture) {
				delete(f.repos.byOrg[f.orgId], "firmware-artifacts")
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"releases[0].artifact.source.repository", "repository not found"},
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts", "firmware-signatures"},
		},
		{
			name: "When a literal signature repository belongs to another organization it should reject it",
			fixture: func(t *testing.T, f *admissionFixture) {
				delete(f.repos.byOrg[f.orgId], "firmware-signatures")
				f.repos.add(t, uuid.New(), "firmware-signatures")
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"signature.source.repository", "repository not found"},
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts", "firmware-signatures"},
		},
		{
			name: "When only a designated target GUID is templated it should accept the fleet and check every literal value",
			mutate: func(firmware *domain.FirmwareSpec) {
				admissionConfig(firmware, "resources", 0)["targetGuid"] = guidTemplate
			},
			wantCode:    http.StatusCreated,
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts", "firmware-signatures"},
		},
		{
			name: "When a templated target GUID has an unknown literal sibling field it should reject it",
			mutate: func(firmware *domain.FirmwareSpec) {
				admissionConfig(firmware, "resources", 0)["targetGuid"] = guidTemplate
				admissionConfig(firmware, "resources", 0)["flash"] = true
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"spec.firmware.updater.config"},
			wantLookups: []string{releaseLookup},
		},
		{
			name: "When a templated target GUID has an invalid literal GUID beside it it should reject it",
			mutate: func(firmware *domain.FirmwareSpec) {
				admissionConfig(firmware, "resources", 0)["targetGuid"] = guidTemplate
				admissionConfig(firmware, "resources", 1)["targetGuid"] = "not-a-guid"
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"resources[1].targetGuid"},
			wantLookups: []string{releaseLookup},
		},
		{
			name: "When a templated target GUID has a missing literal repository beside it it should reject it",
			fixture: func(t *testing.T, f *admissionFixture) {
				delete(f.repos.byOrg[f.orgId], "firmware-artifacts")
			},
			mutate: func(firmware *domain.FirmwareSpec) {
				admissionConfig(firmware, "resources", 0)["targetGuid"] = guidTemplate
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"releases[0].artifact.source.repository", "repository not found"},
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts", "firmware-signatures"},
		},
		{
			name: "When a configuration path that is not designated as templatable uses a template it should reject it",
			mutate: func(firmware *domain.FirmwareSpec) {
				admissionConfig(firmware, "resources", 0)["component"] = "{{ .metadata.labels.component }}"
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"resources[0].component"},
			wantLookups: []string{releaseLookup},
		},
		{
			name: "When a designated repository is templated it should defer it and still check the literal repository",
			mutate: func(firmware *domain.FirmwareSpec) {
				admissionConfig(firmware, "overlays", 0, "artifact", "authentication", "signature", "source")["repository"] = "{{ .metadata.labels.signatures }}"
			},
			wantCode:    http.StatusCreated,
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts"},
		},
		{
			name: "When the updater identity is templated it should accept the fleet without release or repository lookups",
			mutate: func(firmware *domain.FirmwareSpec) {
				firmware.Updater.Version = "{{ .metadata.labels.updaterVersion }}"
			},
			wantCode: http.StatusCreated,
		},
		{
			name:     "When the updater identity is templated and releases are unavailable it should still accept the fleet",
			fixture:  func(t *testing.T, f *admissionFixture) { f.descriptors = nil },
			mutate:   func(firmware *domain.FirmwareSpec) { firmware.Updater.Name = "{{ .metadata.labels.updater }}" },
			wantCode: http.StatusCreated,
		},
		{
			name: "When the updater identity is templated and a component name is templated it should reject it without lookups",
			mutate: func(firmware *domain.FirmwareSpec) {
				firmware.Updater.Version = "{{ .metadata.labels.updaterVersion }}"
				firmware.Components[0].Name = "{{ .metadata.labels.component }}"
			},
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"spec.firmware.components[0].name"},
		},
		{
			name:        "When no repository lookup is configured it should fail closed for literal repositories",
			fixture:     func(t *testing.T, f *admissionFixture) { f.repos = nil },
			wantCode:    http.StatusInternalServerError,
			wantErr:     []string{"repository lookup is unavailable"},
			wantLookups: []string{releaseLookup},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var fixtureOpts []func(t *testing.T, f *admissionFixture)
			if tt.fixture != nil {
				fixtureOpts = append(fixtureOpts, tt.fixture)
			}
			f := newAdmissionFixture(t, fixtureOpts...)
			firmware := admissionFirmware(t)
			if tt.mutate != nil {
				tt.mutate(firmware)
			}
			if tt.noFirmware {
				firmware = nil
			}
			fleet := admissionFleet("fleet-1", firmware)
			original := deepCopyFleet(&fleet)

			_, status := f.svc.CreateFleet(ctx, f.orgId, fleet)

			require.Equal(t, tt.wantCode, status.Code, status.Message)
			for _, want := range tt.wantErr {
				assert.Contains(t, status.Message, want)
			}
			if f.descriptors != nil {
				assert.Equal(t, tt.wantLookups, f.descriptors.lookups)
			}
			if f.repos != nil {
				assert.Equal(t, tt.wantGets, f.repos.gets)
			}
			_, stored := f.store.fleets["fleet-1"]
			assert.Equal(t, status.Code == http.StatusCreated, stored)
			assert.Equal(t, original, &fleet, "the request must not be modified")
			assert.Zero(t, f.store.replaceRepositoryRefsCalls, "admission must not write fleet references")
		})
	}
}

func TestFleetFirmwareAdmissionDeferredChecks(t *testing.T) {
	ctx := context.Background()

	t.Run("When only a designated target GUID is templated it should defer exactly that value's checks", func(t *testing.T) {
		f := newAdmissionFixture(t)
		firmware := admissionFirmware(t)
		admissionConfig(firmware, "resources", 0)["targetGuid"] = guidTemplate

		admission, status := f.svc.admitFirmware(ctx, f.orgId, "fleet-1", nil, &domain.DeviceSpec{Firmware: firmware}, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		guidPath := "spec.firmware.updater.config.resources[0].targetGuid"
		assert.Equal(t, []common.DeferredFirmwareCheck{
			{Path: guidPath, Check: "duplicate-target", DependsOn: common.FirmwareDependsOnTemplateValue},
			{Path: guidPath, Check: "format:uuid", DependsOn: common.FirmwareDependsOnTemplateValue},
		}, admission.Deferred)
		assert.Equal(t, []string{"firmware-artifacts", "firmware-signatures"}, admission.ConcreteRepositoryRefs)
	})

	t.Run("When the updater identity is templated it should defer the release-dependent checks", func(t *testing.T) {
		f := newAdmissionFixture(t)
		firmware := admissionFirmware(t)
		firmware.Updater.Name = "{{ .metadata.labels.updater }}"

		admission, status := f.svc.admitFirmware(ctx, f.orgId, "fleet-1", nil, &domain.DeviceSpec{Firmware: firmware}, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		configPath := "spec.firmware.updater.config"
		assert.Equal(t, []common.DeferredFirmwareCheck{
			{Path: "spec.firmware.updater", Check: "release-authorization", DependsOn: common.FirmwareDependsOnUpdaterIdentity},
			{Path: configPath, Check: "config-schema", DependsOn: common.FirmwareDependsOnUpdaterIdentity},
			{Path: configPath, Check: "repository-reference", DependsOn: common.FirmwareDependsOnUpdaterIdentity},
			{Path: configPath, Check: "target-guid", DependsOn: common.FirmwareDependsOnUpdaterIdentity},
			{Path: configPath, Check: "template-placement", DependsOn: common.FirmwareDependsOnUpdaterIdentity},
		}, admission.Deferred)
		assert.Empty(t, admission.ConcreteRepositoryRefs)
		assert.Empty(t, f.descriptors.lookups)
	})
}

func TestReplaceFleetFirmwareAdmission(t *testing.T) {
	ctx := context.Background()

	t.Run("When the replacement omits the template firmware it should keep the stored firmware without lookups", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeFleet(t, admissionFleet("fleet-1", admissionFirmware(t)))
		f.resetRecords()

		_, status := f.svc.ReplaceFleet(ctx, f.orgId, "fleet-1", admissionFleet("fleet-1", nil), true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Spec.Template.Spec.Firmware, f.store.fleets["fleet-1"].Spec.Template.Spec.Firmware)
		assert.Empty(t, f.descriptors.lookups)
	})

	t.Run("When the replacement omits the firmware but changes the template it should validate the kept firmware", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeFleet(t, admissionFleet("fleet-1", admissionFirmware(t)))
		f.resetRecords()
		replacement := admissionFleet("fleet-1", nil)
		replacement.Spec.Template.Spec.Os.Image = "quay.io/example/os:2"

		_, status := f.svc.ReplaceFleet(ctx, f.orgId, "fleet-1", replacement, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Spec.Template.Spec.Firmware, f.store.fleets["fleet-1"].Spec.Template.Spec.Firmware)
		assert.Equal(t, "quay.io/example/os:2", f.store.fleets["fleet-1"].Spec.Template.Spec.Os.Image)
		assert.Equal(t, []string{releaseLookup}, f.descriptors.lookups)
	})

	t.Run("When removal semantics omit the template firmware it should withdraw the stored firmware", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.storeFleet(t, admissionFleet("fleet-1", admissionFirmware(t)))
		f.resetRecords()

		result, status := f.svc.ReplaceFleet(ctx, f.orgId, "fleet-1", admissionFleet("fleet-1", nil), true, WithOmittedFirmwareRemoval())

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		require.NotNil(t, result)
		assert.Nil(t, result.Spec.Template.Spec.Firmware)
		assert.Nil(t, f.store.fleets["fleet-1"].Spec.Template.Spec.Firmware)
		assert.Empty(t, f.descriptors.lookups)
	})

	t.Run("When a synchronized replacement omits the firmware it should keep the stored firmware", func(t *testing.T) {
		f := newAdmissionFixture(t)
		owned := admissionFleet("fleet-1", admissionFirmware(t))
		owned.Metadata.Owner = lo.ToPtr("ResourceSync/sync-1")
		before := f.storeFleet(t, owned)
		f.resetRecords()
		replacement := admissionFleet("fleet-1", nil)
		replacement.Metadata.Owner = lo.ToPtr("ResourceSync/sync-1")

		_, status := f.svc.ReplaceFleet(ctx, f.orgId, "fleet-1", replacement, false)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Spec.Template.Spec.Firmware, f.store.fleets["fleet-1"].Spec.Template.Spec.Firmware)
	})

	t.Run("When an owned fleet is replaced without firmware and an unchanged template it should not report an ownership conflict", func(t *testing.T) {
		f := newAdmissionFixture(t)
		owned := admissionFleet("fleet-1", admissionFirmware(t))
		owned.Metadata.Owner = lo.ToPtr("ResourceSync/sync-1")
		f.storeFleet(t, owned)
		f.resetRecords()

		_, status := f.svc.ReplaceFleet(ctx, f.orgId, "fleet-1", admissionFleet("fleet-1", nil), true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.NotNil(t, f.store.fleets["fleet-1"].Spec.Template.Spec.Firmware)
	})

	t.Run("When the replacement firmware is invalid it should leave the fleet unchanged", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeFleet(t, admissionFleet("fleet-1", nil))
		f.resetRecords()
		firmware := admissionFirmware(t)
		admissionConfig(firmware, "resources", 0)["targetGuid"] = "not-a-guid"

		_, status := f.svc.ReplaceFleet(ctx, f.orgId, "fleet-1", admissionFleet("fleet-1", firmware), true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, "resources[0].targetGuid")
		assert.Equal(t, before, f.store.fleets["fleet-1"])
	})

	t.Run("When the request creates the fleet it should validate the literal firmware", func(t *testing.T) {
		f := newAdmissionFixture(t)

		_, status := f.svc.ReplaceFleet(ctx, f.orgId, "fleet-1", admissionFleet("fleet-1", admissionFirmware(t)), true)

		require.Equal(t, int32(http.StatusCreated), status.Code, status.Message)
		assert.Equal(t, []string{releaseLookup}, f.descriptors.lookups)
		assert.Zero(t, f.store.replaceRepositoryRefsCalls)
	})

	t.Run("When the request would create the fleet with an invalid literal target GUID it should create nothing", func(t *testing.T) {
		f := newAdmissionFixture(t)
		firmware := admissionFirmware(t)
		admissionConfig(firmware, "resources", 0)["targetGuid"] = "not-a-guid"

		_, status := f.svc.ReplaceFleet(ctx, f.orgId, "fleet-1", admissionFleet("fleet-1", firmware), true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, "resources[0].targetGuid")
		assert.NotContains(t, f.store.fleets, "fleet-1")
		assert.Zero(t, f.store.replaceRepositoryRefsCalls)
	})
}

func TestPatchFleetFirmwareAdmission(t *testing.T) {
	ctx := context.Background()
	firmwareValue := func(t *testing.T) map[string]any {
		var value map[string]any
		require.NoError(t, json.Unmarshal([]byte(admissionFirmwareJSON), &value))
		return value
	}

	t.Run("When a patch adds literal firmware it should validate the patched template", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.storeFleet(t, admissionFleet("fleet-1", nil))

		_, status := f.svc.PatchFleet(ctx, f.orgId, "fleet-1", domain.PatchRequest{{Op: "add", Path: "/spec/template/spec/firmware", Value: firmwareValue(t)}}, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		// Once for the early check of the request and once for the write.
		assert.Equal(t, []string{releaseLookup, releaseLookup}, f.descriptors.lookups)
		assert.NotNil(t, f.store.fleets["fleet-1"].Spec.Template.Spec.Firmware)
	})

	t.Run("When a patch sets the template firmware to null it should keep the stored firmware", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeFleet(t, admissionFleet("fleet-1", admissionFirmware(t)))

		_, status := f.svc.PatchFleet(ctx, f.orgId, "fleet-1", domain.PatchRequest{{Op: "replace", Path: "/spec/template/spec/firmware", Value: nil}}, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Spec.Template.Spec.Firmware, f.store.fleets["fleet-1"].Spec.Template.Spec.Firmware)
		assert.Empty(t, f.descriptors.lookups)
	})

	t.Run("When a patch removes the template firmware it should withdraw it", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.storeFleet(t, admissionFleet("fleet-1", admissionFirmware(t)))

		_, status := f.svc.PatchFleet(ctx, f.orgId, "fleet-1", domain.PatchRequest{{Op: "remove", Path: "/spec/template/spec/firmware"}}, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Nil(t, f.store.fleets["fleet-1"].Spec.Template.Spec.Firmware)
		assert.Empty(t, f.descriptors.lookups)
	})

	t.Run("When a patch adds a field that differs from a firmware field only in case it should reject it", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeFleet(t, admissionFleet("fleet-1", admissionFirmware(t)))

		_, status := f.svc.PatchFleet(ctx, f.orgId, "fleet-1", domain.PatchRequest{{Op: "add", Path: "/spec/template/spec/firmware/Profile", Value: "other"}}, true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, `spec.template.spec.firmware: property "Profile" is unsupported`)
		assert.Equal(t, before, f.store.fleets["fleet-1"])
		assert.Empty(t, f.descriptors.lookups)
	})

	t.Run("When a patch adds an unknown updater configuration field it should reject it", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeFleet(t, admissionFleet("fleet-1", admissionFirmware(t)))

		_, status := f.svc.PatchFleet(ctx, f.orgId, "fleet-1", domain.PatchRequest{{Op: "add", Path: "/spec/template/spec/firmware/updater/config/flash", Value: true}}, true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, "flash")
		assert.Equal(t, before, f.store.fleets["fleet-1"])
	})

	t.Run("When the release is revoked between the early check and the write it should reject the patch as a bad request", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.descriptors.revokeAfter = 1
		before := f.storeFleet(t, admissionFleet("fleet-1", nil))

		_, status := f.svc.PatchFleet(ctx, f.orgId, "fleet-1", domain.PatchRequest{{Op: "add", Path: "/spec/template/spec/firmware", Value: firmwareValue(t)}}, true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, "is not authorized")
		assert.Equal(t, before, f.store.fleets["fleet-1"])
	})

	t.Run("When a patch templates the updater identity it should accept it without lookups", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.storeFleet(t, admissionFleet("fleet-1", admissionFirmware(t)))
		f.resetRecords()

		_, status := f.svc.PatchFleet(ctx, f.orgId, "fleet-1", domain.PatchRequest{{Op: "replace", Path: "/spec/template/spec/firmware/updater/version", Value: "{{ .metadata.labels.updaterVersion }}"}}, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Empty(t, f.descriptors.lookups)
		assert.Empty(t, f.repos.gets)
	})
}
