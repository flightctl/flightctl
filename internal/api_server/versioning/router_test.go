package versioning

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestDispatcher_RoutesToCorrectVersion(t *testing.T) {
	// Create version-specific router
	v1beta1Router := chi.NewRouter()
	v1beta1Router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Routed-Version", "v1beta1")
		w.WriteHeader(http.StatusOK)
	})

	dispatcher, err := newDispatcher(map[Version]chi.Router{
		V1Beta1: v1beta1Router,
	}, V1Beta1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Create request with version in context
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	ctx := ContextWithVersion(req.Context(), V1Beta1)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	dispatcher.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status code = %v, want %v", rec.Code, http.StatusOK)
	}

	if rec.Header().Get("X-Routed-Version") != "v1beta1" {
		t.Errorf("routed version = %v, want v1beta1", rec.Header().Get("X-Routed-Version"))
	}
}

func TestDispatcher_UsesDefaultVersionWhenNotInContext(t *testing.T) {
	v1beta1Router := chi.NewRouter()
	v1beta1Router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Routed-Version", "v1beta1")
		w.WriteHeader(http.StatusOK)
	})

	dispatcher, err := newDispatcher(map[Version]chi.Router{
		V1Beta1: v1beta1Router,
	}, V1Beta1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Create request without version in context
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)

	rec := httptest.NewRecorder()
	dispatcher.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("status code = %v, want %v", rec.Code, http.StatusOK)
	}

	if rec.Header().Get("X-Routed-Version") != "v1beta1" {
		t.Errorf("routed version = %v, want v1beta1", rec.Header().Get("X-Routed-Version"))
	}
}

func TestDispatcher_ReturnsErrorWhenVersionNotFound(t *testing.T) {
	v1beta1Router := chi.NewRouter()
	v1beta1Router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Routed-Version", "v1beta1")
		w.WriteHeader(http.StatusOK)
	})

	dispatcher, err := newDispatcher(map[Version]chi.Router{
		V1Beta1: v1beta1Router,
	}, V1Beta1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Create request with unknown version in context
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	ctx := ContextWithVersion(req.Context(), Version("v999"))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	dispatcher.ServeHTTP(rec, req)

	// Should return 500 error instead of silently falling back
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status code = %v, want %v", rec.Code, http.StatusInternalServerError)
	}
}

func TestDispatcher_ErrorsWhenFallbackVersionMissing(t *testing.T) {
	v1beta1Router := chi.NewRouter()

	// This should return an error because V1Beta1 is the fallback but there's no router for it
	_, err := newDispatcher(map[Version]chi.Router{
		Version("v2"): v1beta1Router,
	}, V1Beta1)
	if err == nil {
		t.Error("expected error when fallback version has no router")
	}
}

func TestDispatcher_ErrorsWhenFallbackVersionEmpty(t *testing.T) {
	v1beta1Router := chi.NewRouter()

	// This should return an error because fallback version is empty
	_, err := newDispatcher(map[Version]chi.Router{
		V1Beta1: v1beta1Router,
	}, "")
	if err == nil {
		t.Error("expected error when fallback version is empty")
	}
}
