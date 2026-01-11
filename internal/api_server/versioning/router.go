package versioning

import (
	"net/http"

	v1api "github.com/flightctl/flightctl/api/v1"
	"github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/api/server"
	"github.com/flightctl/flightctl/internal/api/v1server"
	"github.com/go-chi/chi/v5"
	oapimiddleware "github.com/oapi-codegen/nethttp-middleware"
)

// OapiOptions contains OpenAPI middleware options.
type OapiOptions struct {
	ErrorHandler      func(w http.ResponseWriter, message string, statusCode int)
	MultiErrorHandler oapimiddleware.MultiErrorHandler
}

// CreateV1Router creates a chi.Router for v1 API with OpenAPI validation.
// Routes are auto-registered via the generated v1server.HandlerFromMux.
func CreateV1Router(handler v1server.ServerInterface, opts *OapiOptions) (chi.Router, error) {
	swagger, err := v1api.GetSwagger()
	if err != nil {
		return nil, err
	}

	router := chi.NewRouter()

	oapiOpts := oapimiddleware.Options{
		SilenceServersWarning: true, // Disable Host header validation for servers.url
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

	v1server.HandlerFromMux(handler, router)

	return router, nil
}

// CreateV1Beta1Router creates a chi.Router for v1beta1 API with OpenAPI validation.
// Routes are auto-registered via the generated server.HandlerFromMux.
func CreateV1Beta1Router(handler server.ServerInterface, opts *OapiOptions) (chi.Router, error) {
	swagger, err := v1beta1.GetSwagger()
	if err != nil {
		return nil, err
	}

	router := chi.NewRouter()

	oapiOpts := oapimiddleware.Options{
		SilenceServersWarning: true, // Disable Host header validation for servers.url
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

	server.HandlerFromMux(handler, router)

	return router, nil
}
