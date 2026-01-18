package versioning

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Dispatcher routes requests to version-specific handlers
type Dispatcher struct {
	routers  map[Version]chi.Router
	registry *Registry
}

// NewDispatcher creates a dispatcher with the given version routers
func NewDispatcher(registry *Registry, routers map[Version]chi.Router) *Dispatcher {
	return &Dispatcher{
		routers:  routers,
		registry: registry,
	}
}

// ServeHTTP routes to the appropriate version-specific router
func (d *Dispatcher) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	version, ok := VersionFromContext(r.Context())
	if !ok {
		version = d.registry.FallbackVersion()
	}

	router, exists := d.routers[version]
	if !exists {
		router = d.routers[d.registry.FallbackVersion()]
	}

	if router == nil {
		http.Error(w, "internal server error: no router for version", http.StatusInternalServerError)
		return
	}

	router.ServeHTTP(w, r)
}
