package v1beta1

import (
	_ "embed"

	"github.com/flightctl/flightctl/pkg/openapi"
	"github.com/getkin/kin-openapi/openapi3"
)

//go:embed openapi.yaml
var specYAML []byte

// spec is the core v1beta1 OpenAPI document. It has no external references.
var spec = openapi.NewDocument(specYAML, nil)

// SpecYAML returns the raw core v1beta1 OpenAPI document.
//
// Other API packages reference this specification with external "$ref"s and
// resolve them from these bytes, so the reference target is the same document
// that is compiled into the binary rather than a file read at runtime.
// Callers must not modify the returned slice.
func SpecYAML() []byte {
	return spec.Raw()
}

// GetSpec returns the OpenAPI specification for the core v1beta1 API.
//
// The document is parsed on first use and the result is cached, so every
// caller shares one *openapi3.T. Treat it as read-only; a caller that needs
// to adjust it (clearing Servers before building a router, say) must copy
// it first.
func GetSpec() (*openapi3.T, error) {
	return spec.Spec()
}

// GetSwagger returns the OpenAPI specification for the core v1beta1 API.
//
// Deprecated: GetSwagger predates kin-openapi renaming openapi3.Swagger to
// openapi3.T. Use [GetSpec] instead. This wrapper is retained for backwards
// compatibility.
func GetSwagger() (*openapi3.T, error) {
	return GetSpec()
}
