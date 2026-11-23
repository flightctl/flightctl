package firmware_test

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/device/firmware"
	policy "github.com/flightctl/flightctl/internal/firmwarepolicy"
	"github.com/flightctl/flightctl/test/util/firmwarecatalog"
	"github.com/stretchr/testify/require"
)

func catalogDescriptor(t *testing.T) *policy.ReleaseDescriptor {
	t.Helper()
	cfg, err := firmwarecatalog.New()
	require.NoError(t, err)
	data, err := json.Marshal(cfg.Releases[0])
	require.NoError(t, err)
	d, err := policy.DecodeDescriptor(policy.Binding{Name: cfg.Releases[0].Name, Version: cfg.Releases[0].Version, DescriptorDigest: policy.Hash(data), Descriptor: data})
	require.NoError(t, err)
	return d
}

func TestSignaturePolicy(t *testing.T) {
	d := catalogDescriptor(t)
	keyData := d.Publisher.Policy.KeyData

	t.Run("When rendered for an approved release it should produce the exact default-reject policy", func(t *testing.T) {
		require := require.New(t)
		data, err := firmware.SignaturePolicy(d)
		require.NoError(err)
		require.Equal(`{"default":[{"type":"reject"}],"transports":{"docker":{"quay.io/example/updater":[{"type":"sigstoreSigned","keyData":"`+keyData+`","signedIdentity":{"type":"exactRepository","dockerRepository":"quay.io/example/updater"}}]}}}`, string(data))
	})

	t.Run("When rendered it should admit only the publisher key for the exact repository", func(t *testing.T) {
		require := require.New(t)
		data, err := firmware.SignaturePolicy(d)
		require.NoError(err)
		var rendered struct {
			Default    []map[string]any                       `json:"default"`
			Transports map[string]map[string][]map[string]any `json:"transports"`
		}
		require.NoError(json.Unmarshal(data, &rendered))
		require.Equal([]map[string]any{{"type": "reject"}}, rendered.Default)
		require.Len(rendered.Transports, 1)
		scopes := rendered.Transports["docker"]
		require.Len(scopes, 1)
		requirements := scopes[d.Publisher.Policy.SignedIdentity.DockerRepository]
		require.Len(requirements, 1)
		require.Equal("sigstoreSigned", requirements[0]["type"])
		require.Equal(keyData, requirements[0]["keyData"])
		require.Equal(map[string]any{"type": "exactRepository", "dockerRepository": "quay.io/example/updater"}, requirements[0]["signedIdentity"])
	})

	rejected := []struct {
		name   string
		mutate func(d *policy.ReleaseDescriptor)
	}{
		{"When the policy type is not sigstoreSigned it should refuse to render", func(d *policy.ReleaseDescriptor) { d.Publisher.Policy.Type = "insecureAcceptAnything" }},
		{"When the signed identity is not exactRepository it should refuse to render", func(d *policy.ReleaseDescriptor) { d.Publisher.Policy.SignedIdentity.Type = "matchRepository" }},
		{"When the key data is empty it should refuse to render", func(d *policy.ReleaseDescriptor) { d.Publisher.Policy.KeyData = "" }},
		{"When the repository is empty it should refuse to render", func(d *policy.ReleaseDescriptor) { d.Publisher.Policy.SignedIdentity.DockerRepository = "" }},
	}
	for _, tc := range rejected {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			copied := *catalogDescriptor(t)
			tc.mutate(&copied)
			data, err := firmware.SignaturePolicy(&copied)
			require.Error(err)
			require.Nil(data)
		})
	}

	t.Run("When the descriptor is nil it should refuse to render", func(t *testing.T) {
		data, err := firmware.SignaturePolicy(nil)
		require.Error(t, err)
		require.Nil(t, data)
	})
}
