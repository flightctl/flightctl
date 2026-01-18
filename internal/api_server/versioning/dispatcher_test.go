package versioning

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestDispatcher_RoutesToCorrectVersion(t *testing.T) {
	registry := NewRegistry(V1Beta1)

	// Create version-specific router
	v1beta1Router := chi.NewRouter()
	v1beta1Router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Routed-Version", "v1beta1")
		w.WriteHeader(http.StatusOK)
	})

	dispatcher := NewDispatcher(registry, map[Version]chi.Router{
		V1Beta1: v1beta1Router,
	})

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
	registry := NewRegistry(V1Beta1)

	v1beta1Router := chi.NewRouter()
	v1beta1Router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Routed-Version", "v1beta1")
		w.WriteHeader(http.StatusOK)
	})

	dispatcher := NewDispatcher(registry, map[Version]chi.Router{
		V1Beta1: v1beta1Router,
	})

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

func TestDispatcher_FallsBackToDefaultWhenVersionNotFound(t *testing.T) {
	registry := NewRegistry(V1Beta1)

	v1beta1Router := chi.NewRouter()
	v1beta1Router.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Routed-Version", "v1beta1")
		w.WriteHeader(http.StatusOK)
	})

	dispatcher := NewDispatcher(registry, map[Version]chi.Router{
		V1Beta1: v1beta1Router,
	})

	// Create request with unknown version in context
	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	ctx := ContextWithVersion(req.Context(), Version("v999"))
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	dispatcher.ServeHTTP(rec, req)

	// Should fall back to default version
	if rec.Code != http.StatusOK {
		t.Errorf("status code = %v, want %v", rec.Code, http.StatusOK)
	}

	if rec.Header().Get("X-Routed-Version") != "v1beta1" {
		t.Errorf("routed version = %v, want v1beta1", rec.Header().Get("X-Routed-Version"))
	}
}

func TestDispatcher_ReturnsErrorWhenNoRouterAvailable(t *testing.T) {
	registry := NewRegistry(V1Beta1)

	// Create dispatcher with no routers
	dispatcher := NewDispatcher(registry, map[Version]chi.Router{})

	req := httptest.NewRequest(http.MethodGet, "/devices", nil)
	ctx := ContextWithVersion(req.Context(), V1Beta1)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	dispatcher.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status code = %v, want %v", rec.Code, http.StatusInternalServerError)
	}
}
