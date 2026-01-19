package versioning

import (
	agent "github.com/flightctl/flightctl/api/agent/v1beta1"
	agentserver "github.com/flightctl/flightctl/internal/api/server/agent"
	"github.com/go-chi/chi/v5"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
)

// CreateAgentV1Beta1Router creates a chi.Router for agent v1beta1 API with OpenAPI validation.
// Routes are auto-registered via the generated agentserver.HandlerFromMux.
// Routes are registered without a prefix; chi mount and swagger Servers handle /api/v1.
// Each version has its own swagger spec for independent schema validation.
func CreateAgentV1Beta1Router(handler agentserver.ServerInterface, opts *oapimiddleware.Options) (chi.Router, error) {
	swagger, err := agent.GetSwagger()
	if err != nil {
		return nil, err
	}
	// Keep swagger.Servers intact with /api/v1 base path. OpenAPI validator uses this to:
	// - Match full paths (strips base, matches path)
	// - Fall back to direct matching for stripped paths

	router := chi.NewRouter()

	oapiOpts := oapimiddleware.Options{
		SilenceServersWarning: true, // Suppress Host header mismatch warnings
	}
	if opts != nil {
		if opts.ErrorHandler != nil {
			oapiOpts.ErrorHandler = opts.ErrorHandler
		}
		if opts.MultiErrorHandler != nil {
			oapiOpts.MultiErrorHandler = opts.MultiErrorHandler
		}
	}
	router.Use(oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapiOpts))

	agentserver.HandlerFromMux(handler, router)

	return router, nil
}
