package dependency

import (
	"math"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/stretchr/testify/require"
)

func TestDeltaTargetSelectionIgnoresReportedDigests(t *testing.T) {
	first := &OCIDeltaTarget{Application: "a", CurrentDigest: "sha256:z"}
	second := &OCIDeltaTarget{Application: "b", CurrentDigest: "sha256:a"}
	require.False(t, deltaTargetLess(second, first))
	require.Equal(t, "a", mergeOCIDeltaTargets(first, second).Application)
	require.Equal(t, []string{"a", "b"}, mergeOCIDeltaTargets(first, second).Applications)
	second.Hint = "quay.io/acme/delta:hint"
	require.True(t, deltaTargetLess(second, first))
}

func TestApplicationDeltaPayloadSize(t *testing.T) {
	tests := []struct {
		name      string
		config    *client.OCIDescriptor
		layers    []client.OCIDescriptor
		want      int64
		wantError bool
	}{
		{name: "When a delta has several payload blobs it should include the config and all layers", config: &client.OCIDescriptor{Digest: "config", Size: 2}, layers: []client.OCIDescriptor{{Digest: "a", Size: 10}, {Digest: "b", Size: 20}}, want: 32},
		{name: "When a payload blob is referenced twice it should count the transfer once", config: &client.OCIDescriptor{Digest: "config", Size: 2}, layers: []client.OCIDescriptor{{Digest: "a", Size: 10}, {Digest: "a", Size: 10}}, want: 12},
		{name: "When the manifest lacks a config it should reject the candidate", wantError: true},
		{name: "When a blob has no digest it should reject the candidate", config: &client.OCIDescriptor{Size: 2}, wantError: true},
		{name: "When a blob has a negative size it should reject the candidate", config: &client.OCIDescriptor{Digest: "config", Size: -1}, wantError: true},
		{name: "When the total payload size overflows it should reject the candidate", config: &client.OCIDescriptor{Digest: "config", Size: 2}, layers: []client.OCIDescriptor{{Digest: "a", Size: math.MaxInt64}}, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			size, err := applicationDeltaPayloadSize(&client.OCIManifest{Config: tc.config, Layers: tc.layers})
			if tc.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.want, size)
			}
		})
	}
}
