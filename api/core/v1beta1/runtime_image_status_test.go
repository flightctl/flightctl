package v1beta1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestApplicationVolumeRuntimeDigestJSON(t *testing.T) {
	for _, digest := range []string{"", "sha256:runtime"} {
		t.Run("When the runtime digest is "+digest+" it should preserve optional volume status", func(t *testing.T) {
			require := require.New(t)
			volume := ApplicationVolumeStatus{Name: "data", Reference: "quay.io/acme/data:latest", Digest: digest}
			data, err := json.Marshal(volume)
			require.NoError(err)
			var decoded ApplicationVolumeStatus
			require.NoError(json.Unmarshal(data, &decoded))
			require.Equal(volume, decoded)
			if digest == "" {
				require.NotContains(string(data), `"digest"`)
			} else {
				require.Contains(string(data), `"digest":"sha256:runtime"`)
			}
		})
	}
	spec, err := GetSpec()
	require.NoError(t, err)
	require.Contains(t, spec.Components.Schemas["ApplicationVolumeStatus"].Value.Properties, "digest")
}
