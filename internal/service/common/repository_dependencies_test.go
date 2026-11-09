package common

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testConfigProviders returns git, http, inline and Kubernetes secret
// configuration providers, decoded as the API decodes them.
func testConfigProviders(t *testing.T, documents ...string) *[]domain.ConfigProviderSpec {
	t.Helper()
	providers := make([]domain.ConfigProviderSpec, 0, len(documents))
	for _, document := range documents {
		var provider domain.ConfigProviderSpec
		require.NoError(t, json.Unmarshal([]byte(document), &provider))
		providers = append(providers, provider)
	}
	return &providers
}

const (
	gitProviderOnA    = `{"name": "git-a", "gitRef": {"repository": "config-a", "targetRevision": "main", "path": "/etc"}}`
	gitProviderOnB    = `{"name": "git-b", "gitRef": {"repository": "config-b", "targetRevision": "main", "path": "/opt"}}`
	httpProviderOnA   = `{"name": "http-a", "httpRef": {"repository": "config-a", "filePath": "/etc/http.conf"}}`
	httpProviderOnFW  = `{"name": "http-fw", "httpRef": {"repository": "firmware-artifacts", "filePath": "/etc/fw.conf"}}`
	inlineProvider    = `{"name": "inline", "inline": [{"path": "/etc/motd", "content": "hello"}]}`
	secretProvider    = `{"name": "secret", "secretRef": {"name": "s", "namespace": "ns", "mountPath": "/etc/secret"}}`
	undecodedProvider = `{"name": "unknown"}`
)

func TestCollectConfigRepositoryRefs(t *testing.T) {
	tests := []struct {
		name   string
		config *[]domain.ConfigProviderSpec
		want   []string
	}{
		{name: "When there is no configuration it should return no references", config: nil, want: nil},
		{
			name:   "When git and http providers use repositories it should return each provider's repository in order",
			config: testConfigProviders(t, gitProviderOnB, inlineProvider, httpProviderOnA, secretProvider, gitProviderOnA),
			want:   []string{"config-b", "config-a", "config-a"},
		},
		{
			name:   "When only inline and secret providers exist it should return no references",
			config: testConfigProviders(t, inlineProvider, secretProvider),
			want:   nil,
		},
		{
			name:   "When a provider type cannot be determined it should skip it",
			config: testConfigProviders(t, undecodedProvider, gitProviderOnA),
			want:   []string{"config-a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, CollectConfigRepositoryRefs(tt.config))
		})
	}
}

func TestRepositoryDependencySet(t *testing.T) {
	tests := []struct {
		name     string
		config   []string
		firmware []string
		want     []string
	}{
		{name: "When both sources are empty it should return an empty set", want: []string{}},
		{
			name:     "When the sources overlap it should list a shared repository once, sorted",
			config:   []string{"config-b", "shared", "config-b"},
			firmware: []string{"shared", "firmware-artifacts"},
			want:     []string{"config-b", "firmware-artifacts", "shared"},
		},
		{
			name:     "When only firmware references exist it should return them",
			firmware: []string{"firmware-signatures", "firmware-artifacts"},
			want:     []string{"firmware-artifacts", "firmware-signatures"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RepositoryDependencySet(tt.config, tt.firmware))
		})
	}
}

func TestCollectRepositoryDependencies(t *testing.T) {
	ctx := context.Background()
	specWith := func(t *testing.T, firmware *domain.FirmwareSpec, providers ...string) *domain.DeviceSpec {
		spec := &domain.DeviceSpec{Firmware: firmware}
		if len(providers) > 0 {
			spec.Config = testConfigProviders(t, providers...)
		}
		return spec
	}

	t.Run("When the spec has no firmware it should return the configuration references without a release lookup", func(t *testing.T) {
		provider := newStubProvider()
		names, complete, err := CollectRepositoryDependencies(ctx, provider, specWith(t, nil, gitProviderOnB, httpProviderOnA, gitProviderOnA), FirmwareStandalone)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Equal(t, []string{"config-a", "config-b"}, names)
		assert.Empty(t, provider.lookups)
	})

	t.Run("When the spec has neither configuration nor firmware it should return an empty complete set", func(t *testing.T) {
		names, complete, err := CollectRepositoryDependencies(ctx, nil, &domain.DeviceSpec{}, FirmwareStandalone)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Equal(t, []string{}, names)
	})

	t.Run("When there is no spec it should return an empty complete set", func(t *testing.T) {
		names, complete, err := CollectRepositoryDependencies(ctx, nil, nil, FirmwareStandalone)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Equal(t, []string{}, names)
	})

	t.Run("When firmware and configuration share a repository it should list it once with the firmware-only references", func(t *testing.T) {
		names, complete, err := CollectRepositoryDependencies(ctx, newStubProvider(newTestDescriptor()), specWith(t, newTestFirmware(t), gitProviderOnA, httpProviderOnFW), FirmwareStandalone)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Equal(t, []string{"config-a", "firmware-artifacts", "firmware-signatures"}, names)
	})

	t.Run("When the release is no longer authorized it should still track its references", func(t *testing.T) {
		descriptor := newTestDescriptor()
		descriptor.Approved = false
		names, complete, err := CollectRepositoryDependencies(ctx, newStubProvider(descriptor), specWith(t, newTestFirmware(t)), FirmwareStandalone)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Equal(t, []string{"firmware-artifacts", "firmware-signatures"}, names)
	})

	t.Run("When a fleet template has a templated firmware reference it should leave the placeholder out", func(t *testing.T) {
		firmware := newTestFirmware(t)
		configObject(firmware, "overlays", 0, "artifact", "authentication", "signature", "source")["repository"] = "{{ .metadata.labels.signatures }}"
		names, complete, err := CollectRepositoryDependencies(ctx, newStubProvider(newTestDescriptor()), specWith(t, firmware, gitProviderOnB), FirmwareFleetTemplate)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Equal(t, []string{"config-b", "firmware-artifacts"}, names)
	})

	t.Run("When a fleet template has a templated updater identity it should return only the configuration references", func(t *testing.T) {
		firmware := newTestFirmware(t)
		firmware.Updater.Name = "{{ .metadata.labels.updater }}"
		provider := newStubProvider(newTestDescriptor())
		names, complete, err := CollectRepositoryDependencies(ctx, provider, specWith(t, firmware, gitProviderOnB), FirmwareFleetTemplate)
		require.NoError(t, err)
		assert.True(t, complete)
		assert.Equal(t, []string{"config-b"}, names)
		assert.Empty(t, provider.lookups)
	})

	t.Run("When the release descriptor is unknown it should report an incomplete set", func(t *testing.T) {
		names, complete, err := CollectRepositoryDependencies(ctx, newStubProvider(), specWith(t, newTestFirmware(t), gitProviderOnA), FirmwareStandalone)
		require.NoError(t, err)
		assert.False(t, complete)
		assert.Nil(t, names)
	})

	t.Run("When release descriptors are unavailable it should report an incomplete set", func(t *testing.T) {
		names, complete, err := CollectRepositoryDependencies(ctx, UnavailableDescriptorProvider{}, specWith(t, newTestFirmware(t), gitProviderOnA), FirmwareStandalone)
		require.NoError(t, err)
		assert.False(t, complete)
		assert.Nil(t, names)
	})

	t.Run("When the release lookup fails it should return the error", func(t *testing.T) {
		provider := newStubProvider()
		provider.err = errors.New("descriptor backend timeout")
		names, complete, err := CollectRepositoryDependencies(ctx, provider, specWith(t, newTestFirmware(t)), FirmwareStandalone)
		require.Error(t, err)
		assert.False(t, complete)
		assert.Nil(t, names)
	})
}

func TestRepositoryRefsOutcomeString(t *testing.T) {
	assert.Equal(t, "replaced", RepositoryRefsReplaced.String())
	assert.Equal(t, "stale", RepositoryRefsStale.String())
	assert.Equal(t, "incomplete", RepositoryRefsIncomplete.String())
	assert.Equal(t, "not written", RepositoryRefsOutcome(0).String())
}
