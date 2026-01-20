package versioning

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/go-chi/chi/v5"
)

func TestNewNegotiator(t *testing.T) {
	negotiator := NewNegotiator(V1Beta1)
	if negotiator.FallbackVersion() != V1Beta1 {
		t.Errorf("FallbackVersion() = %v, want %v", negotiator.FallbackVersion(), V1Beta1)
	}
}

func withChiRouteContext(req *http.Request, pattern string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.RoutePatterns = []string{pattern}
	rctx.RoutePath = pattern
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func TestNegotiator_NegotiateMiddleware(t *testing.T) {
	negotiator := NewNegotiator(V1Beta1)

	tests := []struct {
		name                  string
		requested             Version
		routePath             string
		wantVersion           Version
		wantStatusCode        int
		wantVaryHeader        string
		wantSupportedVersions string
	}{
		{
			name:                  "no version requested uses first from metadata",
			requested:             "",
			routePath:             "/devices",
			wantVersion:           V1Beta1,
			wantStatusCode:        http.StatusOK,
			wantVaryHeader:        HeaderAPIVersion,
			wantSupportedVersions: "",
		},
		{
			name:                  "requested v1beta1 succeeds",
			requested:             V1Beta1,
			routePath:             "/devices",
			wantVersion:           V1Beta1,
			wantStatusCode:        http.StatusOK,
			wantVaryHeader:        HeaderAPIVersion,
			wantSupportedVersions: "",
		},
		{
			name:                  "unsupported version returns error",
			requested:             "v2",
			routePath:             "/devices",
			wantVersion:           "",
			wantStatusCode:        http.StatusNotAcceptable,
			wantSupportedVersions: string(V1Beta1),
		},
		{
			name:                  "nil metadata with no version requested returns fallback",
			requested:             "",
			routePath:             "/unknown/path",
			wantVersion:           V1Beta1,
			wantStatusCode:        http.StatusOK,
			wantVaryHeader:        HeaderAPIVersion,
			wantSupportedVersions: "",
		},
		{
			name:                  "nil metadata with fallback version requested returns success",
			requested:             V1Beta1,
			routePath:             "/unknown/path",
			wantVersion:           V1Beta1,
			wantStatusCode:        http.StatusOK,
			wantVaryHeader:        HeaderAPIVersion,
			wantSupportedVersions: "",
		},
		{
			name:                  "nil metadata with non-fallback version requested returns error",
			requested:             "v2",
			routePath:             "/unknown/path",
			wantVersion:           "",
			wantStatusCode:        http.StatusNotAcceptable,
			wantSupportedVersions: string(V1Beta1),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var capturedVersion Version
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedVersion, _ = VersionFromContext(r.Context())
				w.WriteHeader(http.StatusOK)
			})

			wrapped := negotiator.NegotiateMiddleware(handler)

			req := httptest.NewRequest(http.MethodGet, tt.routePath, nil)
			if tt.requested != "" {
				req.Header.Set(HeaderAPIVersion, string(tt.requested))
			}
			req = withChiRouteContext(req, tt.routePath)

			rec := httptest.NewRecorder()
			wrapped.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatusCode {
				t.Errorf("status code = %v, want %v", rec.Code, tt.wantStatusCode)
				return
			}

			if tt.wantStatusCode == http.StatusOK {
				if capturedVersion != tt.wantVersion {
					t.Errorf("negotiated version = %v, want %v", capturedVersion, tt.wantVersion)
				}

				if tt.wantVaryHeader != "" && rec.Header().Get("Vary") != tt.wantVaryHeader {
					t.Errorf("Vary header = %v, want %v", rec.Header().Get("Vary"), tt.wantVaryHeader)
				}

				if rec.Header().Get(HeaderAPIVersion) != string(tt.wantVersion) {
					t.Errorf("%s header = %v, want %v", HeaderAPIVersion, rec.Header().Get(HeaderAPIVersion), tt.wantVersion)
				}

				// Supported versions header should NOT be present on success
				if rec.Header().Get(HeaderAPIVersionsSupported) != "" {
					t.Errorf("%s header should not be present on success, got %v", HeaderAPIVersionsSupported, rec.Header().Get(HeaderAPIVersionsSupported))
				}
			} else if tt.wantStatusCode == http.StatusNotAcceptable {
				// Supported versions header should be present on 406 errors (when metadata has versions)
				if tt.wantSupportedVersions != "" && rec.Header().Get(HeaderAPIVersionsSupported) != tt.wantSupportedVersions {
					t.Errorf("%s header = %v, want %v", HeaderAPIVersionsSupported, rec.Header().Get(HeaderAPIVersionsSupported), tt.wantSupportedVersions)
				}
			}
		})
	}
}

func TestNegotiator_negotiate(t *testing.T) {
	negotiator := NewNegotiator(V1Beta1)

	t.Run("multi-version preference order", func(t *testing.T) {
		metadata := &server.EndpointMetadata{
			Versions: []server.EndpointMetadataVersion{
				{Version: "v1"},
				{Version: "v1beta1"},
			},
		}

		// No version requested: use first (most preferred)
		negotiated, _, err := negotiator.negotiate("", metadata)
		if err != nil {
			t.Errorf("negotiate() error = %v, want nil", err)
		}
		if negotiated != "v1" {
			t.Errorf("negotiate() = %v, want v1", negotiated)
		}

		// Request v1beta1 explicitly
		negotiated, _, err = negotiator.negotiate(V1Beta1, metadata)
		if err != nil {
			t.Errorf("negotiate() error = %v, want nil", err)
		}
		if negotiated != V1Beta1 {
			t.Errorf("negotiate() = %v, want %v", negotiated, V1Beta1)
		}

		// Request unsupported version
		_, _, err = negotiator.negotiate("v2", metadata)
		if err != ErrNotAcceptable {
			t.Errorf("negotiate() error = %v, want ErrNotAcceptable", err)
		}
	})

	t.Run("empty metadata versions list", func(t *testing.T) {
		metadata := &server.EndpointMetadata{
			Versions: []server.EndpointMetadataVersion{},
		}

		negotiated, _, err := negotiator.negotiate("", metadata)
		if err != nil {
			t.Errorf("negotiate() error = %v, want nil", err)
		}
		if negotiated != V1Beta1 {
			t.Errorf("negotiate() = %v, want %v", negotiated, V1Beta1)
		}
	})

	t.Run("nil metadata", func(t *testing.T) {
		negotiated, _, err := negotiator.negotiate("", nil)
		if err != nil {
			t.Errorf("negotiate() error = %v, want nil", err)
		}
		if negotiated != V1Beta1 {
			t.Errorf("negotiate() = %v, want %v", negotiated, V1Beta1)
		}
	})
}
