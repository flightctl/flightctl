package versioning

import (
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/go-chi/chi/v5"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
)

// RouterConfig configures a version-specific router
type RouterConfig struct {
	Version     Version
	Swagger     *openapi3.T
	Handler     server.ServerInterface
	OapiOptions oapimiddleware.Options
}

// NewRouter creates a chi router with version-specific OpenAPI validation
func NewRouter(cfg RouterConfig) chi.Router {
	r := chi.NewRouter()

	if cfg.Swagger != nil {
		r.Use(oapimiddleware.OapiRequestValidatorWithOptions(cfg.Swagger, &cfg.OapiOptions))
	}

	server.HandlerFromMux(cfg.Handler, r)
	return r
}

// NewNegotiatedRouter creates a router that negotiates the API version
// and dispatches to the appropriate version-specific router.
func NewNegotiatedRouter(registry *Registry, routers map[Version]chi.Router) chi.Router {
	r := chi.NewRouter()
	r.Use(Middleware(registry))
	r.Mount("/", NewDispatcher(registry, routers))
	return r
}
