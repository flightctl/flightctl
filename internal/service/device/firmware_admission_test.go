package device

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
      "properties": {"component": {"type": "string"}, "targetGuid": {"type": "string", "format": "uuid"}}}},
    "releases": {"type": "array", "items": {"type": "object", "additionalProperties": false,
      "properties": {"artifact": {"type": "object", "additionalProperties": false,
        "properties": {"source": {"$ref": "#/$defs/source"}}}}}},
    "overlays": {"type": "array", "items": {"type": "object", "additionalProperties": false,
      "properties": {"artifact": {"type": "object", "additionalProperties": false,
        "properties": {"authentication": {"type": "object", "additionalProperties": false,
          "properties": {"signature": {"type": "object", "additionalProperties": false,
            "properties": {"source": {"$ref": "#/$defs/source"}}}}}}}}}}
  },
  "$defs": {"source": {"type": "object", "additionalProperties": false, "properties": {"repository": {"type": "string"}}}}
}`

const admissionFirmwareJSON = `{
  "profile": "orin-example-profile",
  "updater": {"name": "nvidia-orin", "version": "0.1.0", "config": {
    "resources": [{"component": "system", "targetGuid": "00000000-0000-4000-8000-000000000001"}],
    "releases": [{"artifact": {"source": {"repository": "firmware-artifacts"}}}],
    "overlays": [{"artifact": {"authentication": {"signature": {"source": {"repository": "firmware-signatures"}}}}}]
  }},
  "components": [{"name": "system", "version": {"latest": true}}],
  "overlays": []
}`

// admissionCredential is generated at run time so that tests can check that
// errors never expose repository credentials.
var admissionCredential = "credential-" + uuid.NewString()

func admissionDescriptor() *common.ApprovedDescriptor {
	return &common.ApprovedDescriptor{
		Name: "nvidia-orin", Version: "0.1.0", Approved: true,
		ConfigSchema: json.RawMessage(admissionConfigSchema),
		ReferencePaths: []common.FirmwareReferencePath{
			{Path: "releases[].artifact.source.repository", Kind: common.FirmwareReferenceArtifact},
			{Path: "overlays[].artifact.authentication.signature.source.repository", Kind: common.FirmwareReferenceSignature},
		},
		GUIDPaths: []string{"resources[].targetGuid"},
	}
}

// recordingDescriptors is a test stub of the approved descriptor provider
// that records every release lookup.
type recordingDescriptors struct {
	releases map[string]*common.ApprovedDescriptor
	err      error
	lookups  []string
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
	return p.releases[name+"/"+version], nil
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

func newTenantRepositories(t *testing.T, orgId uuid.UUID, names ...string) *tenantRepositories {
	r := &tenantRepositories{byOrg: map[uuid.UUID]map[string]*domain.Repository{}}
	r.add(t, orgId, "https://artifacts.example.com/", names...)
	return r
}

func (r *tenantRepositories) add(t *testing.T, orgId uuid.UUID, urlPrefix string, names ...string) {
	t.Helper()
	if r.byOrg[orgId] == nil {
		r.byOrg[orgId] = map[string]*domain.Repository{}
	}
	for _, name := range names {
		var spec domain.RepositorySpec
		require.NoError(t, spec.FromHttpRepoSpec(domain.HttpRepoSpec{
			Type:       domain.HttpRepoSpecTypeHttp,
			Url:        urlPrefix + name,
			HttpConfig: &domain.HttpConfig{Username: lo.ToPtr("reader"), Password: lo.ToPtr(admissionCredential)},
		}))
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

// admissionSpec returns a device spec whose configuration reads from the
// firmware artifact repository and a configuration-only repository.
func admissionSpec(t *testing.T, firmware *domain.FirmwareSpec) *domain.DeviceSpec {
	t.Helper()
	var config []domain.ConfigProviderSpec
	require.NoError(t, json.Unmarshal([]byte(`[{"name": "git", "gitRef": {"repository": "firmware-artifacts", "targetRevision": "main", "path": "/etc"}},
		{"name": "http", "httpRef": {"repository": "config-http", "filePath": "/etc/http.conf"}}]`), &config))
	return &domain.DeviceSpec{Config: &config, Firmware: firmware}
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

type admissionFixture struct {
	store       *fakeStore
	events      *fakeEvents
	svc         Service
	descriptors *recordingDescriptors
	repos       *tenantRepositories
	orgId       uuid.UUID
}

func newAdmissionFixture(t *testing.T, opts ...func(*admissionFixture)) *admissionFixture {
	f := &admissionFixture{
		store:       newFakeStore(),
		events:      &fakeEvents{},
		descriptors: newRecordingDescriptors(admissionDescriptor()),
		orgId:       uuid.New(),
	}
	f.repos = newTenantRepositories(t, f.orgId, "firmware-artifacts", "firmware-signatures", "config-http")
	for _, opt := range opts {
		opt(f)
	}
	f.svc = NewDeviceServiceHandler(f.store.device, f.store.catalog, f.store.fleet, f.events, nil, "agent.example.com", logrus.New(),
		WithApprovedDescriptorProvider(f.descriptors), WithRepositoryGetter(f.repos.GetRepository))
	return f
}

// storeDevice stores a device directly, with references, as earlier writes
// would have left it.
func (f *admissionFixture) storeDevice(t *testing.T, name string, spec *domain.DeviceSpec, owner *string, refs ...string) *domain.Device {
	t.Helper()
	status := domain.NewDeviceStatus()
	device := &domain.Device{
		ApiVersion: domain.DeviceAPIVersion, Kind: domain.DeviceKind,
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Owner: owner, Labels: &map[string]string{"site": "lab"}},
		Spec:     spec, Status: &status,
	}
	_, err := f.store.device.Create(context.Background(), f.orgId, device, nil)
	require.NoError(t, err)
	f.store.device.repoRefs[name] = refs
	stored, err := f.store.device.get(name)
	require.NoError(t, err)
	return stored
}

func (f *admissionFixture) resetRecords() {
	f.descriptors.lookups = nil
	f.repos.gets = nil
	f.events.created = nil
	f.store.device.repoRefWrites = 0
}

func (f *admissionFixture) successEvents() []domain.EventReason {
	var reasons []domain.EventReason
	for _, event := range f.events.created {
		if event.Reason == domain.EventReasonResourceCreated || event.Reason == domain.EventReasonResourceUpdated {
			reasons = append(reasons, event.Reason)
		}
	}
	return reasons
}

const releaseLookup = "nvidia-orin/0.1.0"

func newAdmissionDevice(name string, spec *domain.DeviceSpec) domain.Device {
	return domain.Device{
		ApiVersion: domain.DeviceAPIVersion, Kind: domain.DeviceKind,
		Metadata: domain.ObjectMeta{Name: lo.ToPtr(name), Labels: &map[string]string{"site": "lab"}},
		Spec:     spec,
	}
}

func TestCreateDeviceFirmwareAdmission(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		fixture     []func(t *testing.T, f *admissionFixture)
		mutate      func(t *testing.T, f *admissionFixture, spec *domain.DeviceSpec)
		noFirmware  bool
		wantCode    int32
		wantErr     []string
		wantLookups []string
		wantGets    []string
		wantRefs    []string
	}{
		{
			name:       "When the device has no firmware it should be created without release or repository lookups",
			noFirmware: true,
			wantCode:   http.StatusCreated,
		},
		{
			name:        "When the firmware is valid it should create the device with its configuration and firmware references",
			wantCode:    http.StatusCreated,
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts", "firmware-signatures"},
			wantRefs:    []string{"config-http", "firmware-artifacts", "firmware-signatures"},
		},
		{
			name: "When approved releases are unavailable it should reject the firmware",
			fixture: []func(t *testing.T, f *admissionFixture){func(t *testing.T, f *admissionFixture) {
				f.descriptors = nil
			}},
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"spec.firmware.updater", "approved updater releases are unavailable"},
		},
		{
			name: "When the updater release is unknown it should reject the firmware",
			fixture: []func(t *testing.T, f *admissionFixture){func(t *testing.T, f *admissionFixture) {
				f.descriptors = newRecordingDescriptors()
			}},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"updater release nvidia-orin/0.1.0 not found"},
			wantLookups: []string{releaseLookup},
		},
		{
			name: "When the updater release is not authorized it should reject the firmware",
			fixture: []func(t *testing.T, f *admissionFixture){func(t *testing.T, f *admissionFixture) {
				descriptor := admissionDescriptor()
				descriptor.Approved = false
				f.descriptors = newRecordingDescriptors(descriptor)
			}},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"is not authorized"},
			wantLookups: []string{releaseLookup},
		},
		{
			name: "When the release lookup fails it should return an internal error",
			fixture: []func(t *testing.T, f *admissionFixture){func(t *testing.T, f *admissionFixture) {
				f.descriptors.err = errors.New("descriptor backend timeout")
			}},
			wantCode:    http.StatusInternalServerError,
			wantLookups: []string{releaseLookup},
		},
		{
			name: "When the artifact repository does not exist it should reject the reference",
			fixture: []func(t *testing.T, f *admissionFixture){func(t *testing.T, f *admissionFixture) {
				delete(f.repos.byOrg[f.orgId], "firmware-artifacts")
			}},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"spec.firmware.updater.config.releases[0].artifact.source.repository", "repository not found"},
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts", "firmware-signatures"},
		},
		{
			name: "When the signature repository does not exist it should reject the reference",
			fixture: []func(t *testing.T, f *admissionFixture){func(t *testing.T, f *admissionFixture) {
				delete(f.repos.byOrg[f.orgId], "firmware-signatures")
			}},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"spec.firmware.updater.config.overlays[0].artifact.authentication.signature.source.repository", "repository not found"},
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts", "firmware-signatures"},
		},
		{
			name: "When the signature repository belongs to another organization it should reject the reference",
			fixture: []func(t *testing.T, f *admissionFixture){func(t *testing.T, f *admissionFixture) {
				delete(f.repos.byOrg[f.orgId], "firmware-signatures")
				f.repos.add(t, uuid.New(), "https://artifacts.example.com/", "firmware-signatures")
			}},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"signature.source.repository", "repository not found"},
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts", "firmware-signatures"},
		},
		{
			name: "When a referenced repository has no usable URL it should reject it without exposing credentials",
			fixture: []func(t *testing.T, f *admissionFixture){func(t *testing.T, f *admissionFixture) {
				f.repos.byOrg[f.orgId]["firmware-artifacts"] = &domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr("firmware-artifacts")}}
			}},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"releases[0].artifact.source.repository", "no usable URL"},
			wantLookups: []string{releaseLookup},
			wantGets:    []string{"firmware-artifacts", "firmware-signatures"},
		},
		{
			name: "When the updater configuration has an unknown field it should reject it",
			mutate: func(t *testing.T, f *admissionFixture, spec *domain.DeviceSpec) {
				spec.Firmware.Updater.Config["flashImmediately"] = true
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"spec.firmware.updater.config"},
			wantLookups: []string{releaseLookup},
		},
		{
			name: "When a target GUID is invalid it should reject it",
			mutate: func(t *testing.T, f *admissionFixture, spec *domain.DeviceSpec) {
				admissionConfig(spec.Firmware, "resources", 0)["targetGuid"] = "not-a-guid"
			},
			wantCode:    http.StatusBadRequest,
			wantErr:     []string{"resources[0].targetGuid"},
			wantLookups: []string{releaseLookup},
		},
		{
			name: "When a standalone device uses a template parameter it should reject it without lookups",
			mutate: func(t *testing.T, f *admissionFixture, spec *domain.DeviceSpec) {
				spec.Firmware.Updater.Version = "{{ .metadata.labels.version }}"
			},
			wantCode: http.StatusBadRequest,
			wantErr:  []string{"spec.firmware.updater.version"},
		},
		{
			name: "When no repository lookup is configured it should fail closed",
			fixture: []func(t *testing.T, f *admissionFixture){func(t *testing.T, f *admissionFixture) {
				f.repos = nil
			}},
			wantCode:    http.StatusInternalServerError,
			wantErr:     []string{"repository lookup is unavailable"},
			wantLookups: []string{releaseLookup},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &admissionFixture{store: newFakeStore(), events: &fakeEvents{}, descriptors: newRecordingDescriptors(admissionDescriptor()), orgId: uuid.New()}
			f.repos = newTenantRepositories(t, f.orgId, "firmware-artifacts", "firmware-signatures", "config-http")
			for _, opt := range tt.fixture {
				opt(t, f)
			}
			var opts []HandlerOption
			if f.descriptors != nil {
				opts = append(opts, WithApprovedDescriptorProvider(f.descriptors))
			}
			if f.repos != nil {
				opts = append(opts, WithRepositoryGetter(f.repos.GetRepository))
			}
			f.svc = NewDeviceServiceHandler(f.store.device, f.store.catalog, f.store.fleet, f.events, nil, "agent.example.com", logrus.New(), opts...)

			spec := admissionSpec(t, admissionFirmware(t))
			if tt.noFirmware {
				spec.Firmware = nil
			}
			if tt.mutate != nil {
				tt.mutate(t, f, spec)
			}
			device := newAdmissionDevice("foo", spec)

			_, status := f.svc.CreateDevice(ctx, f.orgId, device)

			require.Equal(t, tt.wantCode, status.Code, status.Message)
			for _, want := range tt.wantErr {
				assert.Contains(t, status.Message, want)
			}
			assert.NotContains(t, status.Message, admissionCredential)
			if f.descriptors != nil {
				assert.Equal(t, tt.wantLookups, f.descriptors.lookups)
			}
			if f.repos != nil {
				assert.Equal(t, tt.wantGets, f.repos.gets)
			}
			_, stored := f.store.device.devices["foo"]
			assert.Equal(t, status.Code == http.StatusCreated, stored)
			assert.Equal(t, tt.wantRefs, f.store.device.repoRefs["foo"])
			if status.Code != http.StatusCreated {
				assert.Empty(t, f.events.created)
			}
		})
	}

	t.Run("When writing the references fails it should not create the device", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.store.device.repoRefsWriteErr = errors.New("reference write failed")

		_, status := f.svc.CreateDevice(ctx, f.orgId, newAdmissionDevice("foo", admissionSpec(t, admissionFirmware(t))))

		require.Equal(t, int32(http.StatusInternalServerError), status.Code, status.Message)
		assert.NotContains(t, f.store.device.devices, "foo")
		assert.Empty(t, f.store.device.repoRefs["foo"])
		assert.Empty(t, f.successEvents())
	})
}

func TestReplaceDeviceFirmwareAdmission(t *testing.T) {
	ctx := context.Background()
	completeSet := []string{"config-http", "firmware-artifacts", "firmware-signatures"}

	t.Run("When firmware is added to a standalone device it should validate it and store the complete reference set", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.storeDevice(t, "foo", admissionSpec(t, nil), nil, "config-http", "firmware-artifacts")
		f.resetRecords()

		_, status := f.svc.ReplaceDevice(ctx, f.orgId, "foo", newAdmissionDevice("foo", admissionSpec(t, admissionFirmware(t))), nil, true, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, []string{releaseLookup}, f.descriptors.lookups)
		assert.Equal(t, completeSet, f.store.device.repoRefs["foo"])
		assert.NotNil(t, f.store.device.devices["foo"].Spec.Firmware)
	})

	t.Run("When the request creates the device it should validate the firmware and store the complete reference set", func(t *testing.T) {
		f := newAdmissionFixture(t)

		_, status := f.svc.ReplaceDevice(ctx, f.orgId, "foo", newAdmissionDevice("foo", admissionSpec(t, admissionFirmware(t))), nil, true, true)

		require.Equal(t, int32(http.StatusCreated), status.Code, status.Message)
		assert.Equal(t, []string{releaseLookup}, f.descriptors.lookups)
		assert.Equal(t, completeSet, f.store.device.repoRefs["foo"])
	})

	t.Run("When the request would create the device with firmware of an unknown release it should create nothing", func(t *testing.T) {
		f := newAdmissionFixture(t)
		firmware := admissionFirmware(t)
		firmware.Updater.Version = "0.2.0"

		_, status := f.svc.ReplaceDevice(ctx, f.orgId, "foo", newAdmissionDevice("foo", admissionSpec(t, firmware)), nil, true, true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, "updater release nvidia-orin/0.2.0 not found")
		assert.NotContains(t, f.store.device.devices, "foo")
		assert.Zero(t, f.store.device.repoRefWrites)
		assert.Empty(t, f.successEvents())
	})

	t.Run("When the replacement omits firmware it should keep the stored firmware without lookups or reference writes", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), nil, completeSet...)
		f.resetRecords()

		result, status := f.svc.ReplaceDevice(ctx, f.orgId, "foo", newAdmissionDevice("foo", admissionSpec(t, nil)), nil, true, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Spec.Firmware, result.Spec.Firmware)
		assert.Equal(t, before.Spec.Firmware, f.store.device.devices["foo"].Spec.Firmware)
		assert.Empty(t, f.descriptors.lookups)
		assert.Zero(t, f.store.device.repoRefWrites)
		assert.Equal(t, completeSet, f.store.device.repoRefs["foo"])
	})

	t.Run("When the replacement omits firmware but changes the configuration it should validate the kept firmware and update the references", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), nil, completeSet...)
		f.resetRecords()
		spec := admissionSpec(t, nil)
		spec.Config = &[]domain.ConfigProviderSpec{}

		_, status := f.svc.ReplaceDevice(ctx, f.orgId, "foo", newAdmissionDevice("foo", spec), nil, true, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Spec.Firmware, f.store.device.devices["foo"].Spec.Firmware)
		assert.Equal(t, []string{releaseLookup}, f.descriptors.lookups)
		assert.Equal(t, []string{"firmware-artifacts", "firmware-signatures"}, f.store.device.repoRefs["foo"])
	})

	t.Run("When the replacement firmware is not authorized it should leave the device and references unchanged", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeDevice(t, "foo", admissionSpec(t, nil), nil, "config-http", "firmware-artifacts")
		f.resetRecords()
		firmware := admissionFirmware(t)
		firmware.Updater.Version = "0.2.0"

		_, status := f.svc.ReplaceDevice(ctx, f.orgId, "foo", newAdmissionDevice("foo", admissionSpec(t, firmware)), nil, true, true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, "updater release nvidia-orin/0.2.0 not found")
		assert.Equal(t, before, f.store.device.devices["foo"])
		assert.Equal(t, []string{"config-http", "firmware-artifacts"}, f.store.device.repoRefs["foo"])
		assert.Zero(t, f.store.device.repoRefWrites)
		assert.Empty(t, f.successEvents())
	})

	t.Run("When a fleet-owned device is replaced without firmware and an unchanged spec it should keep the firmware and accept the write", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), lo.ToPtr("Fleet/fleet-1"))
		f.resetRecords()
		replacement := newAdmissionDevice("foo", admissionSpec(t, nil))
		replacement.Metadata.Labels = &map[string]string{"site": "lab", "rack": "r1"}

		_, status := f.svc.ReplaceDevice(ctx, f.orgId, "foo", replacement, nil, true, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Spec.Firmware, f.store.device.devices["foo"].Spec.Firmware)
		assert.Empty(t, f.descriptors.lookups)
		assert.Zero(t, f.store.device.repoRefWrites)
	})

	t.Run("When a fleet-owned device's firmware would change it should reject the write before any lookup", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), lo.ToPtr("Fleet/fleet-1"))
		f.resetRecords()
		firmware := admissionFirmware(t)
		firmware.Profile = "orin-other-profile"

		_, status := f.svc.ReplaceDevice(ctx, f.orgId, "foo", newAdmissionDevice("foo", admissionSpec(t, firmware)), nil, true, true)

		require.Equal(t, int32(http.StatusConflict), status.Code, status.Message)
		assert.Empty(t, f.descriptors.lookups)
	})

	t.Run("When only the labels of a device with firmware change it should not look up the release", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), nil, completeSet...)
		f.resetRecords()
		replacement := newAdmissionDevice("foo", admissionSpec(t, admissionFirmware(t)))
		replacement.Metadata.Labels = &map[string]string{"site": "lab", "rack": "r1"}

		_, status := f.svc.ReplaceDevice(ctx, f.orgId, "foo", replacement, nil, true, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Empty(t, f.descriptors.lookups)
		assert.Zero(t, f.store.device.repoRefWrites)
	})
}

func TestPatchDeviceFirmwareAdmission(t *testing.T) {
	ctx := context.Background()
	completeSet := []string{"config-http", "firmware-artifacts", "firmware-signatures"}
	firmwareValue := func(t *testing.T) map[string]any {
		var value map[string]any
		require.NoError(t, json.Unmarshal([]byte(admissionFirmwareJSON), &value))
		return value
	}

	t.Run("When a patch adds valid firmware it should validate the patched device and store the complete reference set", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.storeDevice(t, "foo", admissionSpec(t, nil), nil, "config-http", "firmware-artifacts")
		f.resetRecords()

		_, status := f.svc.PatchDevice(ctx, f.orgId, "foo", domain.PatchRequest{{Op: "add", Path: "/spec/firmware", Value: firmwareValue(t)}}, true, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		// Once for the early check of the request and once for the write.
		assert.Equal(t, []string{releaseLookup, releaseLookup}, f.descriptors.lookups)
		assert.Equal(t, completeSet, f.store.device.repoRefs["foo"])
		assert.NotNil(t, f.store.device.devices["foo"].Spec.Firmware)
	})

	t.Run("When a patch sets the firmware to null it should keep the stored firmware", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), nil, completeSet...)
		f.resetRecords()

		_, status := f.svc.PatchDevice(ctx, f.orgId, "foo", domain.PatchRequest{{Op: "replace", Path: "/spec/firmware", Value: nil}}, true, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Spec.Firmware, f.store.device.devices["foo"].Spec.Firmware)
		assert.Empty(t, f.descriptors.lookups)
		assert.Equal(t, completeSet, f.store.device.repoRefs["foo"])
	})

	t.Run("When a patch replaces the spec without firmware it should keep the stored firmware and validate it with the new spec", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), nil, completeSet...)
		f.resetRecords()

		_, status := f.svc.PatchDevice(ctx, f.orgId, "foo", domain.PatchRequest{{Op: "replace", Path: "/spec", Value: map[string]any{}}}, true, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Equal(t, before.Spec.Firmware, f.store.device.devices["foo"].Spec.Firmware)
		assert.Equal(t, []string{releaseLookup, releaseLookup}, f.descriptors.lookups)
		assert.Equal(t, []string{"firmware-artifacts", "firmware-signatures"}, f.store.device.repoRefs["foo"])
	})

	t.Run("When a patch removes the firmware it should withdraw it and keep only the configuration references", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), nil, completeSet...)
		f.resetRecords()

		_, status := f.svc.PatchDevice(ctx, f.orgId, "foo", domain.PatchRequest{{Op: "remove", Path: "/spec/firmware"}}, true, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Nil(t, f.store.device.devices["foo"].Spec.Firmware)
		assert.Empty(t, f.descriptors.lookups)
		assert.Equal(t, []string{"config-http", "firmware-artifacts"}, f.store.device.repoRefs["foo"])
	})

	t.Run("When a patch adds a field that differs from a firmware field only in case it should reject it", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), nil, completeSet...)
		f.resetRecords()

		_, status := f.svc.PatchDevice(ctx, f.orgId, "foo", domain.PatchRequest{{Op: "add", Path: "/spec/firmware/updater/Version", Value: "0.2.0"}}, true, true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, `spec.firmware.updater: property "Version" is unsupported`)
		assert.Equal(t, before, f.store.device.devices["foo"])
		assert.Empty(t, f.descriptors.lookups)
	})

	t.Run("When a patch adds an unknown updater configuration field it should keep it for the release schema to reject", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), nil, completeSet...)
		f.resetRecords()

		_, status := f.svc.PatchDevice(ctx, f.orgId, "foo", domain.PatchRequest{{Op: "add", Path: "/spec/firmware/updater/config/flashImmediately", Value: true}}, true, true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Contains(t, status.Message, "flashImmediately")
		assert.Equal(t, before, f.store.device.devices["foo"])
		assert.Equal(t, []string{releaseLookup}, f.descriptors.lookups)
	})

	t.Run("When a patch selects an unauthorized release it should leave the device and references unchanged", func(t *testing.T) {
		f := newAdmissionFixture(t)
		before := f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), nil, completeSet...)
		f.resetRecords()

		_, status := f.svc.PatchDevice(ctx, f.orgId, "foo", domain.PatchRequest{{Op: "replace", Path: "/spec/firmware/updater/version", Value: "0.2.0"}}, true, true)

		require.Equal(t, int32(http.StatusBadRequest), status.Code, status.Message)
		assert.Equal(t, before, f.store.device.devices["foo"])
		assert.Equal(t, completeSet, f.store.device.repoRefs["foo"])
		assert.Zero(t, f.store.device.repoRefWrites)
	})

	t.Run("When a patch only changes labels of a device with firmware it should not look up the release", func(t *testing.T) {
		f := newAdmissionFixture(t)
		f.storeDevice(t, "foo", admissionSpec(t, admissionFirmware(t)), nil, completeSet...)
		f.resetRecords()

		_, status := f.svc.PatchDevice(ctx, f.orgId, "foo", domain.PatchRequest{{Op: "add", Path: "/metadata/labels/rack", Value: "r1"}}, true, true)

		require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
		assert.Empty(t, f.descriptors.lookups)
		assert.Zero(t, f.store.device.repoRefWrites)
	})
}

func TestLegacyUpdateDeviceFirmware(t *testing.T) {
	t.Run("When a legacy writer omits firmware it should preserve the stored intent", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgID := uuid.New()
		holdTestDevice(t, st, orgID, nil, holdTestFirmware(), nil)
		incoming := deepCopyDevice(st.device.devices["dev"])
		incoming.Spec.Firmware = nil
		result, err := svc.UpdateDevice(context.Background(), orgID, "dev", *incoming, nil)
		require.NoError(t, err)
		require.Equal(t, holdTestFirmware(), result.Spec.Firmware)
	})
	t.Run("When a legacy writer introduces an unauthorized updater it should reject the write", func(t *testing.T) {
		st, _, svc := newTestHandler()
		orgID := uuid.New()
		holdTestDevice(t, st, orgID, nil, nil, nil)
		incoming := deepCopyDevice(st.device.devices["dev"])
		incoming.Spec.Firmware = holdTestFirmware()
		_, err := svc.UpdateDevice(context.Background(), orgID, "dev", *incoming, nil)
		require.Error(t, err)
		require.Nil(t, st.device.devices["dev"].Spec.Firmware)
	})
}
