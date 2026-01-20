package versioning

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Middleware is an HTTP middleware function type.
type Middleware func(http.Handler) http.Handler

// RouterConfig configures a version-specific router.
type RouterConfig struct {
	Middlewares    []Middleware // Version-specific middleware (validation)
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
// It returns an error if the fallback version is empty or has no corresponding router.
func NewNegotiatedRouter(
	negotiateMW Middleware,
	routers map[Version]chi.Router,
	fallback Version,
) (chi.Router, error) {
	d, err := newDispatcher(routers, fallback)
	if err != nil {
		return nil, err
	}
	r := chi.NewRouter()
	r.Use(negotiateMW)
	r.Mount("/", d)
	return r, nil
}

// dispatcher routes requests to version-specific handlers.
// It is an internal implementation detail of NewNegotiatedRouter.
type dispatcher struct {
	routers        map[Version]chi.Router
	fallback       Version
	fallbackRouter chi.Router
}

// newDispatcher creates a dispatcher with the given version routers.
// It returns an error if the fallback version is empty or has no corresponding router.
func newDispatcher(routers map[Version]chi.Router, fallback Version) (*dispatcher, error) {
	if fallback == "" {
		return nil, errors.New("versioning: fallback version cannot be empty")
	}
	fallbackRouter := routers[fallback]
	if fallbackRouter == nil {
		return nil, fmt.Errorf("versioning: no router for fallback version %s", fallback)
	}
	return &dispatcher{
		routers:        routers,
		fallback:       fallback,
		fallbackRouter: fallbackRouter,
	}, nil
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
