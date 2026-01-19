package versioning

import (
	"net/http"

	"github.com/getkin/kin-openapi/openapi3"
)

// OapiOptions configures OpenAPI validation behavior
type OapiOptions struct {
	ErrorHandler      func(w http.ResponseWriter, message string, statusCode int)
	MultiErrorHandler func(openapi3.MultiError) (int, error)
}
