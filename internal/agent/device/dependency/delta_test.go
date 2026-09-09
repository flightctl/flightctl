package dependency

import (
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/stretchr/testify/require"
)

func TestSelectApplicationDeltaCandidate(t *testing.T) {
	const (
		target = "quay.io/acme/app@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
		hint   = "quay.io/acme/delta@sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		digest = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
		source = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)

	index := &client.OCIIndex{Manifests: []client.OCIReferrer{
		{ArtifactType: ociDeltaArtifactType, Digest: digest, Annotations: map[string]string{ociDeltaSourceAnnotation: source}},
	}}

	tests := []struct {
		name  string
		delta *OCIDeltaTarget
		index *client.OCIIndex
		want  string
	}{
		{name: "hint wins without discovery", delta: &OCIDeltaTarget{Hint: hint, SourceDigest: source}, want: hint},
		{name: "matching referrer is selected", delta: &OCIDeltaTarget{SourceDigest: source}, index: index, want: "quay.io/acme/app@" + digest},
		{name: "nonmatching referrer is ignored", delta: &OCIDeltaTarget{SourceDigest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}, index: index},
		{name: "missing delta metadata has no candidate", index: index},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, selectApplicationDeltaCandidate(target, tt.delta, tt.index))
		})
	}
}
