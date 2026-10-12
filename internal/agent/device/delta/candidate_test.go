package delta

import (
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/stretchr/testify/require"
)

func TestFromManifest(t *testing.T) {
	const source = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const target = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tests := []struct {
		name       string
		target     string
		wantTarget string
		wantError  bool
	}{
		{name: "When the target annotation is valid it should retain the target digest", target: target, wantTarget: target},
		{name: "When the target annotation is absent it should leave the target digest unknown"},
		{name: "When the target annotation is invalid it should reject the candidate", target: "sha256:invalid", wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			annotations := map[string]string{SourceAnnotation: source}
			if tc.target != "" {
				annotations[TargetAnnotation] = tc.target
			}
			manifest := &client.OCIManifest{
				ArtifactType: ArtifactType,
				Annotations:  annotations,
				Config:       &client.OCIDescriptor{Digest: source, Size: 2},
				Layers:       []client.OCIDescriptor{{Digest: target, Size: 3}},
			}

			candidate, err := FromManifest("registry.example.com/delta@"+target, manifest, 1)
			if tc.wantError {
				require.Error(err)
				return
			}
			require.NoError(err)
			require.Equal(source, candidate.SourceDigest)
			require.Equal(tc.wantTarget, candidate.TargetDigest)
			require.Equal(int64(6), candidate.DownloadSize)
		})
	}
}

func TestSelect(t *testing.T) {
	tests := []struct {
		name       string
		candidates []Candidate
		want       string
	}{
		{
			name: "When only one candidate has a local source it should select that candidate",
			candidates: []Candidate{
				{Reference: "registry.example.com/delta@sha256:a", DownloadSize: 1},
				{Reference: "registry.example.com/delta@sha256:b", DownloadSize: 2, SourceAvailable: true},
			},
			want: "registry.example.com/delta@sha256:b",
		},
		{
			name: "When usable candidates have equal sizes it should choose the lexical reference",
			candidates: []Candidate{
				{Reference: "registry.example.com/delta@sha256:b", DownloadSize: 2, SourceAvailable: true},
				{Reference: "registry.example.com/delta@sha256:a", DownloadSize: 2, SourceAvailable: true},
			},
			want: "registry.example.com/delta@sha256:a",
		},
		{
			name:       "When no candidate has a local source it should return no selection",
			candidates: []Candidate{{Reference: "registry.example.com/delta@sha256:a", DownloadSize: 1}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			require.Equal(tc.want, Select(tc.candidates))
		})
	}
}
