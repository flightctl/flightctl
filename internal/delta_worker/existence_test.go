package delta_worker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/stretchr/testify/require"
)

const (
	testSourceDigest = "sha256:1111111111111111111111111111111111111111111111111111111111111111"
	testTargetDigest = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	testDeltaDigest  = "sha256:3333333333333333333333333333333333333333333333333333333333333333"
)

func TestCheckExistingDelta(t *testing.T) {
	deltaManifest := ocispec.Manifest{
		Config: ocispec.Descriptor{Size: 10},
		Layers: []ocispec.Descriptor{{Size: 20}, {Size: 30}},
	}
	indexSize := int64(9999)
	referrer := ocispec.Descriptor{
		MediaType:    ocispec.MediaTypeImageManifest,
		Digest:       testDeltaDigest,
		Size:         indexSize,
		ArtifactType: ociDeltaArtifactType,
		Annotations: map[string]string{
			ociDeltaSourceAnnotation: testSourceDigest,
		},
	}

	tests := []struct {
		name     string
		handler  http.HandlerFunc
		want     existenceStatus
		wantSize int64
	}{
		{
			name: "When Referrers returns a matching delta it should be found with manifest size_bytes",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{referrer}})
				case strings.Contains(r.URL.Path, "/manifests/"+testDeltaDigest):
					writeJSON(w, http.StatusOK, deltaManifest)
				default:
					http.NotFound(w, r)
				}
			},
			want:     existenceFound,
			wantSize: 60,
		},
		{
			name: "When Referrers 404 and Tag Schema has a matching delta it should be found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					http.NotFound(w, r)
				case strings.Contains(r.URL.Path, "/manifests/"+tagSchemaRef(testTargetDigest)):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{referrer}})
				case strings.Contains(r.URL.Path, "/manifests/"+testDeltaDigest):
					writeJSON(w, http.StatusOK, deltaManifest)
				default:
					http.NotFound(w, r)
				}
			},
			want:     existenceFound,
			wantSize: 60,
		},
		{
			name: "When Referrers 200 is empty it should be not found without Tag Schema",
			handler: func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.Contains(r.URL.Path, "/referrers/"):
					writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{}})
				case strings.Contains(r.URL.Path, "/manifests/"+tagSchemaRef(testTargetDigest)):
					t.Errorf("Tag Schema must not be queried after a Referrers 200")
					http.NotFound(w, r)
				default:
					http.NotFound(w, r)
				}
			},
			want: existenceNotFound,
		},
		{
			name: "When Referrers and Tag Schema both 404 it should be not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.NotFound(w, r)
			},
			want: existenceNotFound,
		},
		{
			name: "When Referrers returns 401 it should be inconclusive",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
			},
			want: existenceInconclusive,
		},
		{
			name: "When Referrers returns 500 it should be inconclusive",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			want: existenceInconclusive,
		},
		{
			name: "When Referrers lists a non-matching source digest it should be not found",
			handler: func(w http.ResponseWriter, r *http.Request) {
				other := referrer
				other.Annotations = map[string]string{ociDeltaSourceAnnotation: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
				writeJSON(w, http.StatusOK, ocispec.Index{Manifests: []ocispec.Descriptor{other}})
			},
			want: existenceNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := require.New(t)
			srv := httptest.NewServer(tt.handler)
			t.Cleanup(srv.Close)

			u, err := url.Parse(srv.URL)
			req.NoError(err)
			imageRepository := u.Host + "/team-a/os"

			got, err := checkExistingDelta(context.Background(), imageRepository, testSourceDigest, testTargetDigest, existenceConfig{
				Client: srv.Client(),
				Scheme: "http",
			})
			req.NoError(err)
			req.Equal(tt.want, got.Status)
			if tt.want == existenceFound {
				req.Equal(tt.wantSize, got.SizeBytes)
			}
		})
	}
}

func TestCheckExistingDelta_WhenRegistryUnreachableItShouldBeInconclusive(t *testing.T) {
	req := require.New(t)
	got, err := checkExistingDelta(context.Background(), "127.0.0.1:1/team-a/os", testSourceDigest, testTargetDigest, existenceConfig{
		Client: &http.Client{},
		Scheme: "http",
	})
	req.NoError(err)
	req.Equal(existenceInconclusive, got.Status)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
