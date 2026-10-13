package delta

import (
	"encoding/json"
	"testing"

	"github.com/flightctl/flightctl/internal/agent/client"
	"github.com/stretchr/testify/require"
)

func TestFromReferrerManifest(t *testing.T) {
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
				"config":       client.OCIDescriptor{Digest: source, MediaType: "application/vnd.oci.image.config.v1+json", Size: 2},
				"layers":       []client.OCIDescriptor{{Digest: target, MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Size: 3}},
			}
			if tc.includeSubject {
				manifestJSON["subject"] = client.OCIDescriptor{Digest: tc.subjectDigest, MediaType: "application/vnd.oci.image.manifest.v1+json", Size: 100}
			}
			rawManifest, err := json.Marshal(manifestJSON)
			require.NoError(err)
			var manifest client.OCIManifest
			require.NoError(json.Unmarshal(rawManifest, &manifest))

			candidate, err := FromReferrerManifest("registry.example.com/delta@"+target, &manifest, 1)
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

func TestFromReferrerManifestRejectsInvalidDescriptors(t *testing.T) {
	const source = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const target = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	tests := []struct {
		name         string
		mutate       func(*client.OCIManifest)
		manifestSize int64
	}{
		{name: "When the referrer descriptor size is missing it should reject the candidate", manifestSize: 0},
		{
			name: "When the subject media type is missing it should reject the candidate",
			mutate: func(manifest *client.OCIManifest) {
				manifest.Subject.MediaType = ""
			},
			manifestSize: 1,
		},
		{
			name: "When the subject size is missing it should reject the candidate",
			mutate: func(manifest *client.OCIManifest) {
				manifest.Subject.Size = 0
			},
			manifestSize: 1,
		},
		{
			name: "When the config media type is missing it should reject the candidate",
			mutate: func(manifest *client.OCIManifest) {
				manifest.Config.MediaType = ""
			},
			manifestSize: 1,
		},
		{
			name: "When a layer media type is missing it should reject the candidate",
			mutate: func(manifest *client.OCIManifest) {
				manifest.Layers[0].MediaType = ""
			},
			manifestSize: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			manifest := validDeltaManifest(source, target)
			if tc.mutate != nil {
				tc.mutate(manifest)
			}
			_, err := FromReferrerManifest("registry.example.com/delta@"+target, manifest, tc.manifestSize)
			require.Error(t, err)
		})
	}
}

func TestFromHintManifestAllowsUnknownManifestSize(t *testing.T) {
	const source = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const target = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	candidate, err := FromHintManifest("registry.example.com/delta@"+target, validDeltaManifest(source, target))
	require.NoError(t, err)
	require.Equal(t, int64(5), candidate.DownloadSize)
}

func validDeltaManifest(source, target string) *client.OCIManifest {
	return &client.OCIManifest{
		ArtifactType: ArtifactType,
		Annotations:  map[string]string{SourceAnnotation: source},
		Subject:      &client.OCIDescriptor{MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: target, Size: 10},
		Config:       &client.OCIDescriptor{MediaType: "application/vnd.oci.image.config.v1+json", Digest: source, Size: 2},
		Layers:       []client.OCIDescriptor{{MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: target, Size: 3}},
	}
}

func TestSelect(t *testing.T) {
	tests := []struct {
		name       string
		candidates []Candidate
		want       Candidate
	}{
		{
			name: "When only one candidate has a local source it should select that candidate",
			candidates: []Candidate{
				{Reference: "registry.example.com/delta@sha256:a", DownloadSize: 1},
				{Reference: "registry.example.com/delta@sha256:b", DownloadSize: 2, SourceAvailable: true},
			},
			want: Candidate{Reference: "registry.example.com/delta@sha256:b", DownloadSize: 2, SourceAvailable: true},
		},
		{
			name: "When usable candidates have equal sizes it should choose the lexical reference",
			candidates: []Candidate{
				{Reference: "registry.example.com/delta@sha256:b", DownloadSize: 2, SourceAvailable: true},
				{Reference: "registry.example.com/delta@sha256:a", DownloadSize: 2, SourceAvailable: true},
			},
			want: Candidate{Reference: "registry.example.com/delta@sha256:a", DownloadSize: 2, SourceAvailable: true},
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
