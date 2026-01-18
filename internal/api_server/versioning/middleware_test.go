package versioning

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
)

func withChiRouteContext(req *http.Request, pattern string) *http.Request {
	rctx := chi.NewRouteContext()
	rctx.RoutePatterns = []string{pattern}
	ctx := context.WithValue(req.Context(), chi.RouteCtxKey, rctx)
	return req.WithContext(ctx)
}

func TestMiddleware_SetsHeaders(t *testing.T) {
	registry := NewRegistry(V1Beta1)
	middleware := Middleware(registry)

	// Create a test handler
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	// Wrap with middleware
	wrapped := middleware(handler)

	// Create test request
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	req = withChiRouteContext(req, "/devices")

	// Execute request
	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	// Check Vary header
	if rec.Header().Get("Vary") != HeaderAPIVersion {
		t.Errorf("Vary header = %v, want %v", rec.Header().Get("Vary"), HeaderAPIVersion)
	}

	// Check version header (should be default since metadata won't be found)
	if rec.Header().Get(HeaderAPIVersion) != string(V1Beta1) {
		t.Errorf("%s header = %v, want %v", HeaderAPIVersion, rec.Header().Get(HeaderAPIVersion), V1Beta1)
	}
}

func TestMiddleware_RequestedVersionWithMetadata(t *testing.T) {
	registry := NewRegistry(V1Beta1)
	middleware := Middleware(registry)

	var capturedVersion Version
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedVersion, _ = VersionFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	wrapped := middleware(handler)

	// Create test request with version header
	// /devices IS in APIMetadataMap with v1beta1 support
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	req.Header.Set(HeaderAPIVersion, string(V1Beta1))
	req = withChiRouteContext(req, "/devices")

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	// Should succeed since v1beta1 is supported for this endpoint
	if rec.Code != http.StatusOK {
		t.Errorf("status code = %v, want %v", rec.Code, http.StatusOK)
	}

	if capturedVersion != V1Beta1 {
		t.Errorf("captured version = %v, want %v", capturedVersion, V1Beta1)
	}

	// Check supported versions header
	if rec.Header().Get(HeaderAPIVersionsSupported) != string(V1Beta1) {
		t.Errorf("%s header = %v, want %v", HeaderAPIVersionsSupported, rec.Header().Get(HeaderAPIVersionsSupported), V1Beta1)
	}
}

func TestMiddleware_UnsupportedVersionReturns406(t *testing.T) {
	registry := NewRegistry(V1Beta1)
	middleware := Middleware(registry)

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	wrapped := middleware(handler)

	// Request with unsupported version for endpoint that exists in metadata
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	req.Header.Set(HeaderAPIVersion, "v999") // unsupported version
	req = withChiRouteContext(req, "/devices")

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	// Should return 406 because v999 is not supported
	if rec.Code != http.StatusNotAcceptable {
		t.Errorf("status code = %v, want %v", rec.Code, http.StatusNotAcceptable)
	}
}

func TestMiddleware_NoMetadata_NoRequestedVersion(t *testing.T) {
	registry := NewRegistry(V1Beta1)
	middleware := Middleware(registry)

	var capturedVersion Version
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedVersion, _ = VersionFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	})

	wrapped := middleware(handler)

	// Create test request without version header (uses custom path that won't be in metadata)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	req = withChiRouteContext(req, "/api/v1/test")

	rec := httptest.NewRecorder()
	wrapped.ServeHTTP(rec, req)

	// Should succeed with default version
	if rec.Code != http.StatusOK {
		t.Errorf("status code = %v, want %v", rec.Code, http.StatusOK)
	}

	if capturedVersion != V1Beta1 {
		t.Errorf("captured version = %v, want %v", capturedVersion, V1Beta1)
	}
}

func TestFormatRFC8594Date(t *testing.T) {
	testTime := time.Date(2025, 12, 31, 0, 0, 0, 0, time.UTC)
	got := formatRFC8594Date(&testTime)
	want := "Wed, 31 Dec 2025 00:00:00 UTC"
	if got != want {
		t.Errorf("formatRFC8594Date() = %v, want %v", got, want)
	}
}
