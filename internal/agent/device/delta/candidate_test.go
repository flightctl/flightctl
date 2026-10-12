package delta

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/stretchr/testify/require"
)

func TestFromManifest(t *testing.T) {
	const source = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const target = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tests := []struct {
		name           string
		subjectDigest  string
		includeSubject bool
		legacyTarget   string
		wantTarget     string
		wantError      bool
	}{
		{name: "When the OCI subject is valid it should use its target digest", subjectDigest: target, includeSubject: true, wantTarget: target},
		{name: "When the OCI subject is missing it should reject the candidate", legacyTarget: target, wantError: true},
		{name: "When the OCI subject digest is invalid it should reject the candidate", subjectDigest: "sha256:invalid", includeSubject: true, legacyTarget: target, wantError: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			require := require.New(t)
			annotations := map[string]string{SourceAnnotation: source}
			if tc.legacyTarget != "" {
				annotations["io.github.containers.delta.target"] = tc.legacyTarget
			}
			manifestJSON := map[string]any{
				"artifactType": ArtifactType,
				"annotations":  annotations,
				"config":       client.OCIDescriptor{Digest: source, Size: 2},
				"layers":       []client.OCIDescriptor{{Digest: target, Size: 3}},
			}
			if tc.includeSubject {
				manifestJSON["subject"] = client.OCIDescriptor{Digest: tc.subjectDigest}
			}
			rawManifest, err := json.Marshal(manifestJSON)
			require.NoError(err)
			var manifest client.OCIManifest
			require.NoError(json.Unmarshal(rawManifest, &manifest))

			candidate, err := FromManifest("registry.example.com/delta@"+target, &manifest, 1)
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
