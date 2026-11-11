package common

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testUpdaterName    = "nvidia-orin"
	testUpdaterVersion = "0.1.0"
	testFirmwareSHA    = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testGUID           = "00000000-0000-4000-8000-000000000001"
	testOtherGUID      = "00000000-0000-4000-8000-000000000002"
	guidTemplate       = "{{ .metadata.labels.guid }}"
	configPath         = "spec.firmware.updater.config"
)

// testRepositoryCredential is generated at run time so that the tests can
// check that errors never expose repository credentials.
var testRepositoryCredential = "credential-" + uuid.NewString()

// testConfigSchema is a closed updater configuration schema used by the
// test-only descriptors below. It is shaped like an updater that maps
// components to target GUIDs and fetches artifacts from repositories.
const testConfigSchema = `{
  "$schema": "https://json-schema.org/draft/2020-12/schema",
  "type": "object",
  "additionalProperties": false,
  "required": ["resources", "releases"],
  "properties": {
    "resources": {"type": "array", "items": {"$ref": "#/$defs/resource"}},
    "releases": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["component", "version", "versionFormat", "artifact"],
        "properties": {
          "component": {"type": "string", "minLength": 1},
          "version": {"type": "string", "minLength": 1},
          "versionFormat": {"enum": ["nvidia-bsp-triplet"]},
          "profiles": {"type": "array", "items": {"type": "string", "pattern": "^[a-z0-9-]+$"}},
          "artifact": {
            "type": "object",
            "additionalProperties": false,
            "required": ["source", "digest"],
            "properties": {"source": {"$ref": "#/$defs/source"}, "digest": {"$ref": "#/$defs/digest"}}
          }
        }
      }
    },
    "overlays": {
      "type": "array",
      "items": {
        "type": "object",
        "additionalProperties": false,
        "required": ["name", "artifactFormat", "artifact"],
        "properties": {
          "name": {"type": "string", "minLength": 1},
          "artifactFormat": {"enum": ["dtbo", "dtb"]},
          "artifact": {
            "type": "object",
            "additionalProperties": false,
            "required": ["source", "digest", "authentication"],
            "properties": {
              "source": {"$ref": "#/$defs/source"},
              "digest": {"$ref": "#/$defs/digest"},
              "authentication": {
                "type": "object",
                "additionalProperties": false,
                "required": ["type", "signature"],
                "properties": {
                  "type": {"const": "cms-detached"},
                  "signature": {
                    "type": "object",
                    "additionalProperties": false,
                    "required": ["source", "digest"],
                    "properties": {"source": {"$ref": "#/$defs/source"}, "digest": {"$ref": "#/$defs/digest"}}
                  }
                }
              }
            }
          }
        }
      }
    },
    "maxStagingAttempts": {"type": "integer", "minimum": 0}
  },
  "$defs": {
    "resource": {
      "type": "object",
      "additionalProperties": false,
      "required": ["component", "targetGuid"],
      "properties": {
        "component": {"type": "string", "minLength": 1},
        "targetGuid": {"type": "string", "format": "uuid"},
        "aliases": {"type": "array", "items": {"type": "string"}}
      }
    },
    "source": {
      "type": "object",
      "additionalProperties": false,
      "required": ["type", "repository", "suffix"],
      "properties": {
        "type": {"const": "http"},
        "repository": {"type": "string", "minLength": 1},
        "suffix": {"type": "string", "minLength": 1}
      }
    },
    "digest": {"type": "string", "pattern": "^sha256:[a-f0-9]{64}$"}
  }
}`

// testFirmwareJSON is a complete, valid, all-literal firmware specification
// for the descriptor returned by newTestDescriptor.
const testFirmwareJSON = `{
  "profile": "orin-example-profile",
  "updater": {
    "name": "nvidia-orin",
    "version": "0.1.0",
    "config": {
      "resources": [{"component": "system", "targetGuid": "` + testGUID + `"}],
      "releases": [{
        "component": "system",
        "version": "36.5.2",
        "versionFormat": "nvidia-bsp-triplet",
        "profiles": ["orin-example-profile"],
        "artifact": {
          "source": {"type": "http", "repository": "firmware-artifacts", "suffix": "orin/system.Cap"},
          "digest": "` + testFirmwareSHA + `"
        }
      }],
      "overlays": [{
        "name": "camera",
        "artifactFormat": "dtb",
        "artifact": {
          "source": {"type": "http", "repository": "firmware-artifacts", "suffix": "orin/camera.dtb"},
          "digest": "` + testFirmwareSHA + `",
          "authentication": {
            "type": "cms-detached",
            "signature": {
              "source": {"type": "http", "repository": "firmware-signatures", "suffix": "orin/camera.dtb.p7s"},
              "digest": "` + testFirmwareSHA + `"
            }
          }
        }
      }]
    }
  },
  "components": [{"name": "system", "version": {"minimum": "36.5.0", "maximum": "36.4.4"}}],
  "overlays": [{"name": "camera", "compatibleFirmware": [{"component": "system"}]}]
}`

// newTestDescriptor returns a test stub of an approved updater release
// descriptor. Real descriptors come from the approved descriptor provider.
func newTestDescriptor() *ApprovedDescriptor {
	return &ApprovedDescriptor{
		Name:         testUpdaterName,
		Version:      testUpdaterVersion,
		Approved:     true,
		BuildDigest:  testFirmwareSHA,
		SchemaDigest: testFirmwareSHA,
		ConfigSchema: json.RawMessage(testConfigSchema),
		ReferencePaths: []FirmwareReferencePath{
			{Path: "releases[].artifact.source.repository", Kind: FirmwareReferenceArtifact},
			{Path: "overlays[].artifact.source.repository", Kind: FirmwareReferenceArtifact},
			{Path: "overlays[].artifact.authentication.signature.source.repository", Kind: FirmwareReferenceSignature},
		},
		GUIDPaths: []string{"resources[].targetGuid"},
		TemplatablePaths: []string{
			"resources[].targetGuid",
			"resources[].aliases[]",
			"releases[].profiles[]",
			"releases[].artifact.source.repository",
			"releases[].artifact.source.suffix",
			"overlays[].artifact.source.repository",
			"overlays[].artifact.source.suffix",
			"overlays[].artifact.authentication.signature.source.repository",
			"overlays[].artifact.authentication.signature.source.suffix",
		},
	}
}

// withGUIDSchema returns the test descriptor with the targetGuid schema
// replaced, so a templated GUID can be placed under different schema
// constructions.
func withGUIDSchema(t *testing.T, descriptor *ApprovedDescriptor, guidSchema string) *ApprovedDescriptor {
	t.Helper()
	var schema map[string]any
	require.NoError(t, json.Unmarshal(descriptor.ConfigSchema, &schema))
	var replacement any
	require.NoError(t, json.Unmarshal([]byte(guidSchema), &replacement))
	resource := schema["$defs"].(map[string]any)["resource"].(map[string]any)
	resource["properties"].(map[string]any)["targetGuid"] = replacement
	raw, err := json.Marshal(schema)
	require.NoError(t, err)
	descriptor.ConfigSchema = raw
	return descriptor
}

// stubDescriptorProvider is a test stub of ApprovedDescriptorProvider that
// records every release lookup.
type stubDescriptorProvider struct {
	descriptors map[string]*ApprovedDescriptor
	err         error
	lookups     []string
}

func newStubProvider(descriptors ...*ApprovedDescriptor) *stubDescriptorProvider {
	p := &stubDescriptorProvider{descriptors: map[string]*ApprovedDescriptor{}}
	for _, d := range descriptors {
		p.descriptors[d.Name+"/"+d.Version] = d
	}
	return p
}

func (p *stubDescriptorProvider) LookupRelease(_ context.Context, name, version string) (*ApprovedDescriptor, error) {
	p.lookups = append(p.lookups, name+"/"+version)
	if p.err != nil {
		return nil, p.err
	}
	return p.descriptors[name+"/"+version], nil
}

func (p *stubDescriptorProvider) LookupSchemaByDigest(context.Context, string, string) (*RetainedSchemaEvidence, error) {
	return nil, errors.New("retained schema evidence is not used by intent validation")
}

// firmwareRepositoryStore serves Repository resources per organization, like
// the repository service, and records every lookup.
type firmwareRepositoryStore struct {
	repositories map[uuid.UUID]map[string]*domain.Repository
	failure      *domain.Status
	gets         []string
}

func (s *firmwareRepositoryStore) GetRepository(_ context.Context, orgId uuid.UUID, name string) (*domain.Repository, domain.Status) {
	s.gets = append(s.gets, name)
	if s.failure != nil {
		return nil, *s.failure
	}
	repository, ok := s.repositories[orgId][name]
	if !ok {
		return nil, domain.StatusResourceNotFound(domain.RepositoryKind, name)
	}
	return repository, domain.StatusOK()
}

func httpRepository(t *testing.T, name string) *domain.Repository {
	t.Helper()
	var spec domain.RepositorySpec
	require.NoError(t, spec.FromHttpRepoSpec(domain.HttpRepoSpec{
		Type:       domain.HttpRepoSpecTypeHttp,
		Url:        "https://artifacts.example.com/" + name,
		HttpConfig: &domain.HttpConfig{Username: lo.ToPtr("reader"), Password: lo.ToPtr(testRepositoryCredential)},
	}))
	return &domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr(name)}, Spec: spec}
}

func newFirmwareRepositoryStore(t *testing.T, orgId uuid.UUID, names ...string) *firmwareRepositoryStore {
	t.Helper()
	store := &firmwareRepositoryStore{repositories: map[uuid.UUID]map[string]*domain.Repository{orgId: {}}}
	for _, name := range names {
		store.repositories[orgId][name] = httpRepository(t, name)
	}
	return store
}

func newTestFirmware(t *testing.T) *domain.FirmwareSpec {
	t.Helper()
	var firmware domain.FirmwareSpec
	require.NoError(t, json.Unmarshal([]byte(testFirmwareJSON), &firmware))
	return &firmware
}

// configValue returns the decoded configuration object or array element at
// the given keys and indexes.
func configValue(firmware *domain.FirmwareSpec, path ...any) any {
	var current any = firmware.Updater.Config
	for _, step := range path {
		switch s := step.(type) {
		case string:
			current = current.(map[string]any)[s]
		case int:
			current = current.([]any)[s]
		}
	}
	return current
}

func configObject(firmware *domain.FirmwareSpec, path ...any) map[string]any {
	return configValue(firmware, path...).(map[string]any)
}

func deferred(path, check, dependsOn string) DeferredFirmwareCheck {
	return DeferredFirmwareCheck{Path: path, Check: check, DependsOn: dependsOn}
}

type firmwareIntentCase struct {
	name          string
	mode          FirmwareValidationMode
	mutate        func(t *testing.T, firmware *domain.FirmwareSpec)
	descriptor    func(t *testing.T) *ApprovedDescriptor
	providerErr   error
	nilProvider   bool
	unavailable   bool
	repositories  []string
	repositoryErr *domain.Status

	wantCode     int32
	wantErrs     []string
	wantDeferred []DeferredFirmwareCheck
	wantRefs     []string
	wantLookups  []string
	wantGets     []string
}

func runFirmwareIntentCase(t *testing.T, tt firmwareIntentCase) {
	t.Helper()
	orgId := uuid.New()
	firmware := newTestFirmware(t)
	if tt.mutate != nil {
		tt.mutate(t, firmware)
	}
	descriptor := newTestDescriptor()
	if tt.descriptor != nil {
		descriptor = tt.descriptor(t)
	}
	stub := newStubProvider()
	if descriptor != nil {
		stub = newStubProvider(descriptor)
	}
	stub.err = tt.providerErr
	var provider ApprovedDescriptorProvider = stub
	switch {
	case tt.nilProvider:
		provider = nil
	case tt.unavailable:
		provider = UnavailableDescriptorProvider{}
	}
	repositories := tt.repositories
	if repositories == nil {
		repositories = []string{"firmware-artifacts", "firmware-signatures"}
	}
	store := newFirmwareRepositoryStore(t, orgId, repositories...)
	store.failure = tt.repositoryErr

	admission, status := ValidateFirmwareIntent(context.Background(), orgId, provider, store.GetRepository, &domain.DeviceSpec{Firmware: firmware}, tt.mode)

	wantCode := tt.wantCode
	if wantCode == 0 {
		wantCode = http.StatusOK
	}
	require.Equal(t, wantCode, status.Code, "status: %s", status.Message)
	joined := errors.Join(admission.Errors...)
	if len(tt.wantErrs) == 0 {
		assert.Empty(t, admission.Errors, "unexpected errors: %v", joined)
	} else {
		require.NotEmpty(t, admission.Errors)
		for _, text := range tt.wantErrs {
			assert.Contains(t, joined.Error(), text)
			assert.Contains(t, status.Message, text)
		}
	}
	if wantCode != http.StatusOK {
		assert.Empty(t, admission.Deferred, "a rejected request reports no deferred checks")
		assert.Empty(t, admission.ConcreteRepositoryRefs, "a rejected request returns no repository references")
	}
	assert.Equal(t, tt.wantDeferred, nilIfEmpty(admission.Deferred), "deferred checks")
	assert.Equal(t, tt.wantRefs, nilIfEmpty(admission.ConcreteRepositoryRefs), "repository references")
	if !tt.nilProvider && !tt.unavailable {
		assert.Equal(t, tt.wantLookups, nilIfEmpty(stub.lookups), "release lookups")
	}
	assert.Equal(t, tt.wantGets, nilIfEmpty(store.gets), "repository lookups")
	assert.NotContains(t, status.Message, testRepositoryCredential, "errors must not expose repository credentials")
	assert.NotContains(t, status.Message, "artifacts.example.com", "errors must not expose repository URLs")
}

func nilIfEmpty[T any](values []T) []T {
	if len(values) == 0 {
		return nil
	}
	return values
}

var (
	testRelease         = []string{testUpdaterName + "/" + testUpdaterVersion}
	testRepositoryGets  = []string{"firmware-artifacts", "firmware-signatures"}
	testRepositoryRefs  = []string{"firmware-artifacts", "firmware-signatures"}
	testUnavailableText = "spec.firmware.updater: approved updater releases are unavailable"
)

func TestValidateFirmwareIntentResolvesTheUpdaterRelease(t *testing.T) {
	tests := []firmwareIntentCase{
		{
			name:        "When a standalone request is literal and the release is approved it should accept and return the validated references",
			wantRefs:    testRepositoryRefs,
			wantLookups: testRelease,
			wantGets:    testRepositoryGets,
		},
		{
			name:        "When a fleet template is entirely literal it should validate it like a standalone request",
			mode:        FirmwareFleetTemplate,
			wantRefs:    testRepositoryRefs,
			wantLookups: testRelease,
			wantGets:    testRepositoryGets,
		},
		{
			name:        "When the release is unknown it should reject the request",
			descriptor:  func(t *testing.T) *ApprovedDescriptor { return nil },
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{"spec.firmware.updater: updater release nvidia-orin/0.1.0 not found"},
			wantLookups: testRelease,
		},
		{
			name: "When the release is not authorized it should reject the request",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.Approved = false
				return d
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{"spec.firmware.updater: updater release nvidia-orin/0.1.0 is not authorized"},
			wantLookups: testRelease,
		},
		{
			name:        "When the release lookup fails it should report an internal error",
			providerErr: errors.New("descriptor backend timeout"),
			wantCode:    http.StatusInternalServerError,
			wantLookups: testRelease,
		},
		{
			name:        "When no descriptor provider is configured it should fail closed",
			nilProvider: true,
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{testUnavailableText},
		},
		{
			name:        "When the unavailable descriptor provider is configured it should fail closed",
			unavailable: true,
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{testUnavailableText},
		},
		{
			name: "When the descriptor configuration schema is not valid JSON it should fail closed",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.ConfigSchema = json.RawMessage(`{"type": "object",`)
				return d
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: testRelease,
		},
		{
			name: "When the descriptor has no configuration schema it should fail closed",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.ConfigSchema = nil
				return d
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: testRelease,
		},
		{
			name: "When the descriptor schema refers to an external schema it should not load it and fail closed",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.ConfigSchema = json.RawMessage(`{"$ref": "https://schemas.example.com/updater.json"}`)
				return d
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: testRelease,
		},
		{
			name: "When a descriptor path is malformed it should fail closed",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.TemplatablePaths = append(d.TemplatablePaths, "resources..targetGuid")
				return d
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: testRelease,
		},
		{
			name: "When a descriptor reference path has an unknown kind it should fail closed",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.ReferencePaths[0].Kind = "certificate"
				return d
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: testRelease,
		},
		{
			name: "When a descriptor GUID path is malformed it should fail closed",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.GUIDPaths = append(d.GUIDPaths, "resources[]..targetGuid")
				return d
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: testRelease,
		},
		{
			name: "When a descriptor reference path is malformed it should fail closed",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.ReferencePaths = append(d.ReferencePaths, FirmwareReferencePath{Path: "releases[].[]", Kind: FirmwareReferenceArtifact})
				return d
			},
			wantCode:    http.StatusInternalServerError,
			wantLookups: testRelease,
		},
		{
			name:     "When a required firmware field is missing it should reject it before any lookup",
			mutate:   func(t *testing.T, f *domain.FirmwareSpec) { f.Profile = "" },
			wantCode: http.StatusBadRequest,
			wantErrs: []string{"spec.firmware.profile: Required value"},
		},
		{
			name:     "When the literal updater version is empty it should reject it before any lookup",
			mutate:   func(t *testing.T, f *domain.FirmwareSpec) { f.Updater.Version = "" },
			wantCode: http.StatusBadRequest,
			wantErrs: []string{"spec.firmware.updater.version: Required value"},
		},
		{
			name: "When the firmware is structurally invalid it should reject it before any lookup",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				f.Components = append(f.Components, f.Components[0])
			},
			wantCode: http.StatusBadRequest,
			wantErrs: []string{`spec.firmware.components[1].name: Duplicate value: "system"`},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runFirmwareIntentCase(t, tt) })
	}
}

func TestValidateFirmwareIntentDetectsAWrongIdentityDescriptor(t *testing.T) {
	orgId := uuid.New()
	wrong := newTestDescriptor()
	wrong.Version = "0.2.0"
	provider := &stubDescriptorProvider{descriptors: map[string]*ApprovedDescriptor{testUpdaterName + "/" + testUpdaterVersion: wrong}}
	store := newFirmwareRepositoryStore(t, orgId, "firmware-artifacts", "firmware-signatures")

	admission, status := ValidateFirmwareIntent(context.Background(), orgId, provider, store.GetRepository, &domain.DeviceSpec{Firmware: newTestFirmware(t)}, FirmwareStandalone)

	assert.Equal(t, int32(http.StatusInternalServerError), status.Code, status.Message)
	assert.Contains(t, status.Message, "different release")
	assert.Empty(t, admission.ConcreteRepositoryRefs)
	assert.Empty(t, store.gets)
}

func TestValidateFirmwareIntentValidatesTheClosedConfiguration(t *testing.T) {
	tests := []firmwareIntentCase{
		{
			name:        "When the configuration has an unknown top-level field it should reject it",
			mutate:      func(t *testing.T, f *domain.FirmwareSpec) { f.Updater.Config["debugShell"] = true },
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ":", "debugShell"},
			wantLookups: testRelease,
		},
		{
			name: "When a nested configuration object has an unknown field it should reject it at that object",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "releases", 0, "artifact")["mirror"] = "https://mirror.example.com"
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".releases[0].artifact:", "mirror"},
			wantLookups: testRelease,
		},
		{
			name: "When a required configuration field is missing it should reject it",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				delete(configObject(f, "resources", 0), "targetGuid")
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".resources[0]:", "targetGuid"},
			wantLookups: testRelease,
		},
		{
			name: "When a configuration field has the wrong type it should reject it",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "releases", 0)["version"] = 36.5
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".releases[0].version:"},
			wantLookups: testRelease,
		},
		{
			name: "When a literal configuration value breaks its pattern it should reject it",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "releases", 0, "artifact")["digest"] = "sha256:short"
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".releases[0].artifact.digest:"},
			wantLookups: testRelease,
		},
		{
			name: "When a literal configuration value breaks a constant it should reject it",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "releases", 0, "artifact", "source")["type"] = "ftp"
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".releases[0].artifact.source.type:"},
			wantLookups: testRelease,
		},
		{
			name: "When the version format is not supported by the release it should reject it",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "releases", 0)["versionFormat"] = "semver"
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".releases[0].versionFormat:"},
			wantLookups: testRelease,
		},
		{
			name: "When release versions are unordered and constraints are contradictory it should accept them without comparing versions",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				releases := configValue(f, "releases").([]any)
				second := map[string]any{}
				raw, _ := json.Marshal(releases[0])
				_ = json.Unmarshal(raw, &second)
				second["version"] = "35.0.1"
				f.Updater.Config["releases"] = append(releases, second)
			},
			wantRefs:    testRepositoryRefs,
			wantLookups: testRelease,
			wantGets:    testRepositoryGets,
		},
		{
			name: "When a literal target GUID is malformed it should reject it",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "resources", 0)["targetGuid"] = "not-a-guid"
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".resources[0].targetGuid:"},
			wantLookups: testRelease,
		},
		{
			name: "When two resources target the same GUID it should reject the duplicate target",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				f.Updater.Config["resources"] = []any{
					map[string]any{"component": "system", "targetGuid": testGUID},
					map[string]any{"component": "bootloader", "targetGuid": strings.ToUpper(testGUID)},
				}
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".resources[1].targetGuid:", "duplicate target GUID"},
			wantLookups: testRelease,
		},
		{
			name: "When a template appears in a standalone configuration it should reject it before any lookup",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "resources", 0)["targetGuid"] = guidTemplate
			},
			wantCode: http.StatusBadRequest,
			wantErrs: []string{configPath + ".resources[0].targetGuid:", "template parameters are only supported in fleet templates"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runFirmwareIntentCase(t, tt) })
	}
}

func TestValidateFirmwareIntentAuthorizesRepositoryReferences(t *testing.T) {
	tests := []firmwareIntentCase{
		{
			name: "When the release declares no reference paths it should accept the firmware without repository lookups",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.ReferencePaths = nil
				return d
			},
			wantLookups: testRelease,
		},
		{
			name:         "When an artifact repository does not exist in the organization it should reject the reference",
			repositories: []string{"firmware-signatures"},
			wantCode:     http.StatusBadRequest,
			wantErrs: []string{
				configPath + `.releases[0].artifact.source.repository: Invalid value: "firmware-artifacts": repository not found`,
				configPath + `.overlays[0].artifact.source.repository: Invalid value: "firmware-artifacts": repository not found`,
			},
			wantLookups: testRelease,
			wantGets:    testRepositoryGets,
		},
		{
			name:         "When a detached signature repository does not exist it should reject the reference",
			repositories: []string{"firmware-artifacts"},
			wantCode:     http.StatusBadRequest,
			wantErrs:     []string{configPath + `.overlays[0].artifact.authentication.signature.source.repository: Invalid value: "firmware-signatures": repository not found`},
			wantLookups:  testRelease,
			wantGets:     testRepositoryGets,
		},
		{
			name:          "When the repository lookup fails it should report an internal error",
			repositoryErr: lo.ToPtr(domain.StatusInternalServerError("connection refused")),
			wantCode:      http.StatusInternalServerError,
			wantLookups:   testRelease,
			wantGets:      []string{"firmware-artifacts"},
		},
		{
			name: "When the same repository is referenced several times it should look it up once",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "overlays", 0, "artifact", "authentication", "signature", "source")["repository"] = "firmware-artifacts"
			},
			repositories: []string{"firmware-artifacts"},
			wantRefs:     []string{"firmware-artifacts"},
			wantLookups:  testRelease,
			wantGets:     []string{"firmware-artifacts"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runFirmwareIntentCase(t, tt) })
	}
}

func TestValidateFirmwareIntentRepositoryEdgeCases(t *testing.T) {
	orgId := uuid.New()
	provider := newStubProvider(newTestDescriptor())

	t.Run("When the repository exists only in another organization it should be treated as not found", func(t *testing.T) {
		store := newFirmwareRepositoryStore(t, orgId, "firmware-signatures")
		store.repositories[uuid.New()] = map[string]*domain.Repository{"firmware-artifacts": httpRepository(t, "firmware-artifacts")}

		admission, status := ValidateFirmwareIntent(context.Background(), orgId, provider, store.GetRepository, &domain.DeviceSpec{Firmware: newTestFirmware(t)}, FirmwareStandalone)

		assert.Equal(t, int32(http.StatusBadRequest), status.Code)
		assert.Contains(t, status.Message, `"firmware-artifacts": repository not found`)
		assert.Empty(t, admission.ConcreteRepositoryRefs)
	})

	t.Run("When a repository has no usable specification it should reject the reference without exposing it", func(t *testing.T) {
		store := newFirmwareRepositoryStore(t, orgId, "firmware-signatures")
		raw, err := json.Marshal(map[string]any{
			"type":       "ftp",
			"url":        "https://artifacts.example.com/private",
			"httpConfig": map[string]any{"username": "reader", "password": testRepositoryCredential},
		})
		require.NoError(t, err)
		var spec domain.RepositorySpec
		require.NoError(t, json.Unmarshal(raw, &spec))
		store.repositories[orgId]["firmware-artifacts"] = &domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr("firmware-artifacts")}, Spec: spec}

		admission, status := ValidateFirmwareIntent(context.Background(), orgId, provider, store.GetRepository, &domain.DeviceSpec{Firmware: newTestFirmware(t)}, FirmwareStandalone)

		assert.Equal(t, int32(http.StatusBadRequest), status.Code)
		assert.Contains(t, status.Message, `"firmware-artifacts": repository has no usable URL`)
		assert.NotContains(t, status.Message, testRepositoryCredential)
		assert.NotContains(t, status.Message, "artifacts.example.com")
		assert.Empty(t, admission.ConcreteRepositoryRefs)
	})

	t.Run("When a repository record has an empty specification it should reject the reference", func(t *testing.T) {
		store := newFirmwareRepositoryStore(t, orgId, "firmware-signatures")
		store.repositories[orgId]["firmware-artifacts"] = &domain.Repository{Metadata: domain.ObjectMeta{Name: lo.ToPtr("firmware-artifacts")}}

		_, status := ValidateFirmwareIntent(context.Background(), orgId, provider, store.GetRepository, &domain.DeviceSpec{Firmware: newTestFirmware(t)}, FirmwareStandalone)

		assert.Equal(t, int32(http.StatusBadRequest), status.Code)
		assert.Contains(t, status.Message, `"firmware-artifacts": repository has no usable URL`)
	})

	t.Run("When the repository lookup returns no repository it should report an internal error", func(t *testing.T) {
		store := newFirmwareRepositoryStore(t, orgId, "firmware-signatures")
		store.repositories[orgId]["firmware-artifacts"] = nil

		admission, status := ValidateFirmwareIntent(context.Background(), orgId, provider, store.GetRepository, &domain.DeviceSpec{Firmware: newTestFirmware(t)}, FirmwareStandalone)

		assert.Equal(t, int32(http.StatusInternalServerError), status.Code)
		assert.Empty(t, admission.ConcreteRepositoryRefs)
	})

	t.Run("When references exist but no repository lookup is available it should fail closed", func(t *testing.T) {
		admission, status := ValidateFirmwareIntent(context.Background(), orgId, provider, nil, &domain.DeviceSpec{Firmware: newTestFirmware(t)}, FirmwareStandalone)

		assert.Equal(t, int32(http.StatusInternalServerError), status.Code)
		assert.Empty(t, admission.ConcreteRepositoryRefs)
	})

	t.Run("When the device spec has no firmware it should accept it without lookups", func(t *testing.T) {
		store := newFirmwareRepositoryStore(t, orgId)
		stub := newStubProvider()

		admission, status := ValidateFirmwareIntent(context.Background(), orgId, stub, store.GetRepository, &domain.DeviceSpec{}, FirmwareStandalone)

		assert.Equal(t, int32(http.StatusOK), status.Code)
		assert.Equal(t, FirmwareAdmission{}, admission)
		assert.Empty(t, stub.lookups)
		assert.Empty(t, store.gets)
	})
}

func TestValidateFirmwareIntentChecksDescriptorPathsIndependentlyOfTheSchema(t *testing.T) {
	permissiveGUID := func(t *testing.T) *ApprovedDescriptor {
		return withGUIDSchema(t, newTestDescriptor(), `{"type": "string"}`)
	}
	tests := []firmwareIntentCase{
		{
			name:       "When the schema does not constrain a GUID path it should still reject a malformed literal GUID",
			descriptor: permissiveGUID,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "resources", 0)["targetGuid"] = "not-a-guid"
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + `.resources[0].targetGuid: Invalid value: "not-a-guid": must be a GUID`},
			wantLookups: testRelease,
		},
		{
			name: "When the schema does not constrain a GUID path it should reject a value that is not a string",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				return withGUIDSchema(t, newTestDescriptor(), `{}`)
			},
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "resources", 0)["targetGuid"] = 42
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".resources[0].targetGuid: Invalid value: 42: must be a GUID string"},
			wantLookups: testRelease,
		},
		{
			name:       "When the schema does not constrain a templated GUID path it should defer the GUID check",
			mode:       FirmwareFleetTemplate,
			descriptor: permissiveGUID,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "resources", 0)["targetGuid"] = guidTemplate
			},
			wantDeferred: []DeferredFirmwareCheck{deferred(configPath+".resources[0].targetGuid", "target-guid", FirmwareDependsOnTemplateValue)},
			wantRefs:     testRepositoryRefs,
			wantLookups:  testRelease,
			wantGets:     testRepositoryGets,
		},
		{
			name: "When a reference path holds a value that is not a string it should reject it",
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				var schema map[string]any
				require.NoError(t, json.Unmarshal(d.ConfigSchema, &schema))
				source := schema["$defs"].(map[string]any)["source"].(map[string]any)
				source["properties"].(map[string]any)["repository"] = map[string]any{}
				raw, err := json.Marshal(schema)
				require.NoError(t, err)
				d.ConfigSchema = raw
				return d
			},
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "releases", 0, "artifact", "source")["repository"] = 42
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".releases[0].artifact.source.repository: Invalid value: 42: must be a repository name"},
			wantLookups: testRelease,
			wantGets:    testRepositoryGets,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runFirmwareIntentCase(t, tt) })
	}
}

func TestValidateFirmwareIntentDefersOnlyTemplatedValues(t *testing.T) {
	guidPath := configPath + ".resources[0].targetGuid"
	tests := []firmwareIntentCase{
		{
			name: "When an allowed templated GUID is the only failure it should accept and defer exactly its format check",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "resources", 0)["targetGuid"] = guidTemplate
			},
			wantDeferred: []DeferredFirmwareCheck{deferred(guidPath, "format:uuid", FirmwareDependsOnTemplateValue)},
			wantRefs:     testRepositoryRefs,
			wantLookups:  testRelease,
			wantGets:     testRepositoryGets,
		},
		{
			name: "When an allowed templated GUID sits beside an unknown field it should reject the unknown field",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				resource := configObject(f, "resources", 0)
				resource["targetGuid"] = guidTemplate
				resource["foo"] = "bar"
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".resources[0]:", "foo"},
			wantLookups: testRelease,
		},
		{
			name: "When an allowed templated GUID sits beside an invalid literal GUID it should reject the literal",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				f.Updater.Config["resources"] = []any{
					map[string]any{"component": "system", "targetGuid": guidTemplate},
					map[string]any{"component": "bootloader", "targetGuid": "not-a-guid"},
				}
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".resources[1].targetGuid:"},
			wantLookups: testRelease,
		},
		{
			name: "When a templated GUID sits beside a literal GUID it should also defer the duplicate-target comparison",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				f.Updater.Config["resources"] = []any{
					map[string]any{"component": "system", "targetGuid": guidTemplate},
					map[string]any{"component": "bootloader", "targetGuid": testOtherGUID},
				}
			},
			wantDeferred: []DeferredFirmwareCheck{
				deferred(guidPath, "duplicate-target", FirmwareDependsOnTemplateValue),
				deferred(guidPath, "format:uuid", FirmwareDependsOnTemplateValue),
			},
			wantRefs:    testRepositoryRefs,
			wantLookups: testRelease,
			wantGets:    testRepositoryGets,
		},
		{
			name: "When a templated GUID is at a path the release does not designate it should reject it",
			mode: FirmwareFleetTemplate,
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.TemplatablePaths = slices.DeleteFunc(d.TemplatablePaths, func(p string) bool { return p == "resources[].targetGuid" })
				return d
			},
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "resources", 0)["targetGuid"] = guidTemplate
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{guidPath + ":", "template parameters are not permitted at this path"},
			wantLookups: testRelease,
		},
		{
			name: "When a template is placed at a configuration path the release does not designate it should reject it",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "releases", 0)["version"] = "{{ .metadata.labels.release }}"
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".releases[0].version:", "template parameters are not permitted at this path"},
			wantLookups: testRelease,
		},
		{
			name: "When a designated path holds a template in a non-string field it should reject the type error",
			mode: FirmwareFleetTemplate,
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				d.TemplatablePaths = append(d.TemplatablePaths, "maxStagingAttempts")
				return d
			},
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				f.Updater.Config["maxStagingAttempts"] = "{{ .metadata.labels.attempts }}"
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".maxStagingAttempts:"},
			wantLookups: testRelease,
		},
		{
			name: "When a templated artifact repository is designated it should defer it without looking it up",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "releases", 0, "artifact", "source")["repository"] = "{{ .metadata.labels.repository }}"
			},
			wantDeferred: []DeferredFirmwareCheck{deferred(configPath+".releases[0].artifact.source.repository", "repository-reference", FirmwareDependsOnTemplateValue)},
			wantRefs:     testRepositoryRefs,
			wantLookups:  testRelease,
			wantGets:     testRepositoryGets,
		},
		{
			name: "When a templated value passes every current check it should still be listed as deferred",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "releases", 0, "artifact", "source")["suffix"] = "orin/{{ .metadata.labels.board }}.Cap"
			},
			wantDeferred: []DeferredFirmwareCheck{deferred(configPath+".releases[0].artifact.source.suffix", "config-value", FirmwareDependsOnTemplateValue)},
			wantRefs:     testRepositoryRefs,
			wantLookups:  testRelease,
			wantGets:     testRepositoryGets,
		},
		{
			name: "When several templated values fail only deferrable checks it should defer each of them",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				f.Updater.Config["resources"] = []any{
					map[string]any{"component": "system", "targetGuid": guidTemplate},
					map[string]any{"component": "bootloader", "targetGuid": "{{ .metadata.labels.bootGuid }}"},
				}
				configObject(f, "releases", 0)["profiles"] = []any{"{{ .metadata.labels.profile }}"}
			},
			wantDeferred: []DeferredFirmwareCheck{
				deferred(configPath+".releases[0].profiles[0]", "pattern", FirmwareDependsOnTemplateValue),
				deferred(guidPath, "duplicate-target", FirmwareDependsOnTemplateValue),
				deferred(guidPath, "format:uuid", FirmwareDependsOnTemplateValue),
				deferred(configPath+".resources[1].targetGuid", "duplicate-target", FirmwareDependsOnTemplateValue),
				deferred(configPath+".resources[1].targetGuid", "format:uuid", FirmwareDependsOnTemplateValue),
			},
			wantRefs:    testRepositoryRefs,
			wantLookups: testRelease,
			wantGets:    testRepositoryGets,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runFirmwareIntentCase(t, tt) })
	}
}

func TestValidateFirmwareIntentClassifiesSchemaErrorConstructions(t *testing.T) {
	templatedGUID := func(t *testing.T, f *domain.FirmwareSpec) {
		configObject(f, "resources", 0)["targetGuid"] = guidTemplate
	}
	guidPath := configPath + ".resources[0].targetGuid"
	tests := []firmwareIntentCase{
		{
			name: "When the templated GUID fails inside an allOf branch with two failures it should prune every wrapper and accept",
			mode: FirmwareFleetTemplate,
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				return withGUIDSchema(t, newTestDescriptor(), `{"allOf": [{"type": "string", "format": "uuid", "pattern": "^[0-9a-fA-F-]{36}$"}]}`)
			},
			mutate: templatedGUID,
			wantDeferred: []DeferredFirmwareCheck{
				deferred(guidPath, "format:uuid", FirmwareDependsOnTemplateValue),
				deferred(guidPath, "pattern", FirmwareDependsOnTemplateValue),
			},
			wantRefs:    testRepositoryRefs,
			wantLookups: testRelease,
			wantGets:    testRepositoryGets,
		},
		{
			name: "When the allOf-composed item also has an unknown field it should keep the wrappers around that failure and reject",
			mode: FirmwareFleetTemplate,
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				var schema map[string]any
				require.NoError(t, json.Unmarshal(d.ConfigSchema, &schema))
				defs := schema["$defs"].(map[string]any)
				defs["resource"] = map[string]any{"allOf": []any{defs["resource"]}}
				raw, err := json.Marshal(schema)
				require.NoError(t, err)
				d.ConfigSchema = raw
				return d
			},
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				resource := configObject(f, "resources", 0)
				resource["targetGuid"] = guidTemplate
				resource["foo"] = "bar"
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".resources[0]:", "foo"},
			wantLookups: testRelease,
		},
		{
			name: "When the templated GUID fails under anyOf it should keep the composite failure and reject",
			mode: FirmwareFleetTemplate,
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				return withGUIDSchema(t, newTestDescriptor(), `{"anyOf": [{"type": "string", "format": "uuid"}, {"enum": ["unassigned"]}]}`)
			},
			mutate:      templatedGUID,
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{guidPath + ":"},
			wantLookups: testRelease,
		},
		{
			name: "When the templated GUID fails under oneOf it should keep the composite failure and reject",
			mode: FirmwareFleetTemplate,
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				return withGUIDSchema(t, newTestDescriptor(), `{"oneOf": [{"type": "string", "format": "uuid"}, {"const": "unassigned"}]}`)
			},
			mutate:      templatedGUID,
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{guidPath + ":"},
			wantLookups: testRelease,
		},
		{
			name: "When the templated GUID fails in a then branch it should keep the conditional failure and reject",
			mode: FirmwareFleetTemplate,
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				return withGUIDSchema(t, newTestDescriptor(), `{"type": "string", "if": {"type": "string"}, "then": {"format": "uuid"}}`)
			},
			mutate:      templatedGUID,
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{guidPath + ":"},
			wantLookups: testRelease,
		},
		{
			name: "When the templated GUID fails in an else branch it should keep the conditional failure and reject",
			mode: FirmwareFleetTemplate,
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				return withGUIDSchema(t, newTestDescriptor(), `{"type": "string", "if": {"const": "unassigned"}, "else": {"format": "uuid"}}`)
			},
			mutate:      templatedGUID,
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{guidPath + ":"},
			wantLookups: testRelease,
		},
		{
			name: "When a templated value fails a minContains count it should keep the count failure and reject",
			mode: FirmwareFleetTemplate,
			descriptor: func(t *testing.T) *ApprovedDescriptor {
				d := newTestDescriptor()
				var schema map[string]any
				require.NoError(t, json.Unmarshal(d.ConfigSchema, &schema))
				resource := schema["$defs"].(map[string]any)["resource"].(map[string]any)
				resource["properties"].(map[string]any)["aliases"] = map[string]any{
					"type": "array", "contains": map[string]any{"type": "string", "format": "uuid"}, "minContains": 1,
				}
				raw, err := json.Marshal(schema)
				require.NoError(t, err)
				d.ConfigSchema = raw
				return d
			},
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				configObject(f, "resources", 0)["aliases"] = []any{guidTemplate}
			},
			wantCode:    http.StatusBadRequest,
			wantErrs:    []string{configPath + ".resources[0].aliases:"},
			wantLookups: testRelease,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runFirmwareIntentCase(t, tt) })
	}
}

func TestValidateFirmwareIntentWithTemplatedUpdaterIdentity(t *testing.T) {
	identityDeferred := []DeferredFirmwareCheck{
		deferred("spec.firmware.updater", "release-authorization", FirmwareDependsOnUpdaterIdentity),
		deferred(configPath, "config-schema", FirmwareDependsOnUpdaterIdentity),
		deferred(configPath, "repository-reference", FirmwareDependsOnUpdaterIdentity),
		deferred(configPath, "target-guid", FirmwareDependsOnUpdaterIdentity),
		deferred(configPath, "template-placement", FirmwareDependsOnUpdaterIdentity),
	}
	tests := []firmwareIntentCase{
		{
			name: "When the updater version is templated it should accept without a release lookup and defer every descriptor check",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				f.Updater.Version = "{{ .metadata.labels.updaterVersion }}"
				configObject(f, "releases", 0)["version"] = "{{ .metadata.labels.release }}"
			},
			wantDeferred: identityDeferred,
		},
		{
			name: "When the updater identity is templated it should still reject common structural errors",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				f.Updater.Name = "{{ .metadata.labels.updater }}"
				f.Components[0].ArtifactDigest = lo.ToPtr("sha256:short")
			},
			wantCode: http.StatusBadRequest,
			wantErrs: []string{"spec.firmware.components[0].artifactDigest: Invalid value"},
		},
		{
			name: "When the updater identity is templated it should still reject invalid template syntax in the configuration",
			mode: FirmwareFleetTemplate,
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				f.Updater.Name = "{{ .metadata.labels.updater }}"
				configObject(f, "resources", 0)["targetGuid"] = "{{ .metadata.labels.guid"
			},
			wantCode: http.StatusBadRequest,
			wantErrs: []string{configPath + ".resources[0].targetGuid: Invalid value"},
		},
		{
			name: "When a standalone device templates the updater identity it should reject it without a lookup",
			mutate: func(t *testing.T, f *domain.FirmwareSpec) {
				f.Updater.Name = "{{ .metadata.labels.updater }}"
			},
			wantCode: http.StatusBadRequest,
			wantErrs: []string{"spec.firmware.updater.name: Invalid value"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) { runFirmwareIntentCase(t, tt) })
	}
}

func TestExtractFirmwareRepositoryRefs(t *testing.T) {
	ctx := context.Background()

	t.Run("When the release descriptor is available it should return the literal artifact and signature references", func(t *testing.T) {
		provider := newStubProvider(newTestDescriptor())
		refs, complete, err := ExtractFirmwareRepositoryRefs(ctx, provider, newTestFirmware(t), FirmwareStandalone)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Equal(t, []string{"firmware-artifacts", "firmware-signatures"}, refs)
	})

	t.Run("When the release is no longer authorized it should still return its references for dependency tracking", func(t *testing.T) {
		descriptor := newTestDescriptor()
		descriptor.Approved = false
		refs, complete, err := ExtractFirmwareRepositoryRefs(ctx, newStubProvider(descriptor), newTestFirmware(t), FirmwareStandalone)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Equal(t, []string{"firmware-artifacts", "firmware-signatures"}, refs)
	})

	t.Run("When a reference is templated it should leave it out", func(t *testing.T) {
		firmware := newTestFirmware(t)
		configObject(firmware, "overlays", 0, "artifact", "authentication", "signature", "source")["repository"] = "{{ .metadata.labels.signatures }}"
		refs, complete, err := ExtractFirmwareRepositoryRefs(ctx, newStubProvider(newTestDescriptor()), firmware, FirmwareFleetTemplate)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Equal(t, []string{"firmware-artifacts"}, refs)
	})

	t.Run("When the release declares no reference paths it should return a complete empty set", func(t *testing.T) {
		descriptor := newTestDescriptor()
		descriptor.ReferencePaths = nil
		refs, complete, err := ExtractFirmwareRepositoryRefs(ctx, newStubProvider(descriptor), newTestFirmware(t), FirmwareStandalone)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Empty(t, refs)
	})

	t.Run("When a descriptor reference path is malformed it should report an error and no references", func(t *testing.T) {
		descriptor := newTestDescriptor()
		descriptor.ReferencePaths = append(descriptor.ReferencePaths, FirmwareReferencePath{Path: "releases[].[]", Kind: FirmwareReferenceArtifact})
		refs, complete, err := ExtractFirmwareRepositoryRefs(ctx, newStubProvider(descriptor), newTestFirmware(t), FirmwareStandalone)
		require.Error(t, err)
		assert.False(t, complete)
		assert.Nil(t, refs)
	})

	t.Run("When the release is unknown it should report the references as incomplete", func(t *testing.T) {
		refs, complete, err := ExtractFirmwareRepositoryRefs(ctx, newStubProvider(), newTestFirmware(t), FirmwareStandalone)
		require.NoError(t, err)
		assert.False(t, complete)
		assert.Empty(t, refs)
	})

	t.Run("When descriptors are unavailable it should report the references as incomplete", func(t *testing.T) {
		for _, provider := range []ApprovedDescriptorProvider{nil, UnavailableDescriptorProvider{}} {
			refs, complete, err := ExtractFirmwareRepositoryRefs(ctx, provider, newTestFirmware(t), FirmwareStandalone)
			require.NoError(t, err)
			assert.False(t, complete)
			assert.Empty(t, refs)
		}
	})

	t.Run("When the release lookup fails it should return the error", func(t *testing.T) {
		provider := newStubProvider()
		provider.err = errors.New("descriptor backend timeout")
		_, complete, err := ExtractFirmwareRepositoryRefs(ctx, provider, newTestFirmware(t), FirmwareStandalone)
		require.Error(t, err)
		assert.False(t, complete)
	})

	t.Run("When the provider returns a descriptor for a different release it should return an error", func(t *testing.T) {
		wrong := newTestDescriptor()
		wrong.Version = "0.2.0"
		provider := &stubDescriptorProvider{descriptors: map[string]*ApprovedDescriptor{testUpdaterName + "/" + testUpdaterVersion: wrong}}
		_, complete, err := ExtractFirmwareRepositoryRefs(ctx, provider, newTestFirmware(t), FirmwareStandalone)
		require.Error(t, err)
		assert.False(t, complete)
	})

	t.Run("When a fleet template has a templated updater identity it should report no references without a lookup", func(t *testing.T) {
		firmware := newTestFirmware(t)
		firmware.Updater.Name = "{{ .metadata.labels.updater }}"
		provider := newStubProvider(newTestDescriptor())
		refs, complete, err := ExtractFirmwareRepositoryRefs(ctx, provider, firmware, FirmwareFleetTemplate)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Empty(t, refs)
		assert.Empty(t, provider.lookups)
	})

	t.Run("When a standalone device has a templated updater identity it should return an error", func(t *testing.T) {
		firmware := newTestFirmware(t)
		firmware.Updater.Name = "{{ .metadata.labels.updater }}"
		_, complete, err := ExtractFirmwareRepositoryRefs(ctx, newStubProvider(newTestDescriptor()), firmware, FirmwareStandalone)
		require.Error(t, err)
		assert.False(t, complete)
	})

	t.Run("When there is no firmware it should report no references", func(t *testing.T) {
		refs, complete, err := ExtractFirmwareRepositoryRefs(ctx, nil, nil, FirmwareStandalone)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Empty(t, refs)
	})
}

func TestUnavailableDescriptorProviderFailsClosed(t *testing.T) {
	provider := UnavailableDescriptorProvider{}

	descriptor, err := provider.LookupRelease(context.Background(), testUpdaterName, testUpdaterVersion)
	assert.Nil(t, descriptor)
	assert.ErrorIs(t, err, ErrApprovedDescriptorsUnavailable)

	evidence, err := provider.LookupSchemaByDigest(context.Background(), testUpdaterName, testFirmwareSHA)
	assert.Nil(t, evidence)
	assert.ErrorIs(t, err, ErrApprovedDescriptorsUnavailable)
}

func TestFirmwareDeferredChecksAreSorted(t *testing.T) {
	orgId := uuid.New()
	firmware := newTestFirmware(t)
	configObject(firmware, "releases", 0, "artifact", "source")["suffix"] = "{{ .metadata.labels.b }}"
	configObject(firmware, "overlays", 0, "artifact", "source")["suffix"] = "{{ .metadata.labels.a }}"
	store := newFirmwareRepositoryStore(t, orgId, "firmware-artifacts", "firmware-signatures")

	admission, status := ValidateFirmwareIntent(context.Background(), orgId, newStubProvider(newTestDescriptor()), store.GetRepository, &domain.DeviceSpec{Firmware: firmware}, FirmwareFleetTemplate)

	require.Equal(t, int32(http.StatusOK), status.Code, status.Message)
	paths := lo.Map(admission.Deferred, func(d DeferredFirmwareCheck, _ int) string { return d.Path })
	assert.True(t, slices.IsSorted(paths), "deferred checks must be reported in a stable order: %v", paths)
}
