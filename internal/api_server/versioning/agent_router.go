package versioning

import (
	agent "github.com/flightctl/flightctl/api/agent/v1beta1"
	agentserver "github.com/flightctl/flightctl/internal/api/server/agent"
	"github.com/go-chi/chi/v5"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
)

// CreateAgentV1Beta1Router creates a chi.Router for agent v1beta1 API with OpenAPI validation.
// Routes are auto-registered via the generated agentserver.HandlerFromMux.
// Each version has its own swagger spec for independent schema validation.
func CreateAgentV1Beta1Router(handler agentserver.ServerInterface, opts *OapiOptions) (chi.Router, error) {
	swagger, err := agent.GetSwagger()
	if err != nil {
		return nil, err
	}
	// Skip server name validation - Chi strips the /api/v1 prefix before
	// this router sees the request, so server URL matching would fail
	swagger.Servers = nil

	router := chi.NewRouter()

	oapiOpts := oapimiddleware.Options{}
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
