package versioning

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Middleware is an HTTP middleware function type.
type Middleware func(http.Handler) http.Handler

// RouterConfig configures a version-specific router.
type RouterConfig struct {
	Middlewares    []Middleware
	RegisterRoutes func(r chi.Router)
}

// NewRouter creates a chi router with version-specific middleware.
func NewRouter(cfg RouterConfig) chi.Router {
	r := chi.NewRouter()

	for _, mw := range cfg.Middlewares {
		r.Use(mw)
	}

	if cfg.RegisterRoutes != nil {
		cfg.RegisterRoutes(r)
	}
	return r
}

// NewNegotiatedRouter creates a router that negotiates the API version
// and dispatches to the appropriate version-specific router.
func NewNegotiatedRouter(
	negotiateMW Middleware,
	routers map[Version]chi.Router,
	fallback Version,
) chi.Router {
	r := chi.NewRouter()
	r.Use(negotiateMW)
	r.Mount("/", newDispatcher(routers, fallback))
	return r
}

// dispatcher routes requests to version-specific handlers.
// It is an internal implementation detail of NewNegotiatedRouter.
type dispatcher struct {
	routers        map[Version]chi.Router
	fallback       Version
	fallbackRouter chi.Router
}

// newDispatcher creates a dispatcher with the given version routers.
// It panics if the fallback version is empty or has no corresponding router.
func newDispatcher(routers map[Version]chi.Router, fallback Version) *dispatcher {
	if fallback == "" {
		panic("versioning: fallback version cannot be empty")
	}
	fallbackRouter := routers[fallback]
	if fallbackRouter == nil {
		panic("versioning: no router for fallback version " + string(fallback))
	}
	return &dispatcher{
		routers:        routers,
		fallback:       fallback,
		fallbackRouter: fallbackRouter,
	}
}

// ServeHTTP routes to the appropriate version-specific router
func (d *dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v, ok := VersionFromContext(r.Context())
	if !ok || v == "" {
		// No version in context - use fallback.
		// This should never happen when mounted with negotiation middleware,
		// but we handle it gracefully for direct mounting or testing scenarios.
		d.fallbackRouter.ServeHTTP(w, r)
		return
	}

	router := d.routers[v]
	if router == nil {
		// Version in context but no router - this should never happen if the
		// negotiation middleware and router configuration are consistent.
		// Return 500 rather than silently falling back to a different version.
		http.Error(w, "internal server error: no router for negotiated version", http.StatusInternalServerError)
		return
	}

	router.ServeHTTP(w, r)
}
