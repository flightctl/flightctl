package versioning

import (
	agent "github.com/flightctl/flightctl/api/agent/v1beta1"
	agentserver "github.com/flightctl/flightctl/internal/api/server/agent"
	"github.com/go-chi/chi/v5"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
)

// CreateAgentV1Beta1Router creates a chi.Router for agent v1beta1 API with OpenAPI validation.
// Routes are auto-registered via the generated agentserver.HandlerFromMux.
// Chi's Mount strips the /api/v1 prefix before passing to this router, so routes
// are registered without the prefix (e.g., /enrollmentrequests not /api/v1/enrollmentrequests).
// Each version has its own swagger spec for independent schema validation.
func CreateAgentV1Beta1Router(handler agentserver.ServerInterface, opts *oapimiddleware.Options) (chi.Router, error) {
	swagger, err := agent.GetSwagger()
	if err != nil {
		return nil, err
	}
	// Skip server name validation - Chi strips the /api/v1 prefix before
	// this router sees the request, so server URL matching would fail
	swagger.Servers = nil

	router := chi.NewRouter()

	if opts != nil {
		router.Use(oapimiddleware.OapiRequestValidatorWithOptions(swagger, opts))
	} else {
		router.Use(oapimiddleware.OapiRequestValidatorWithOptions(swagger, &oapimiddleware.Options{}))
	}

	agentserver.HandlerFromMux(handler, router)

	return router, nil
}
