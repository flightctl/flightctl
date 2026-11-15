package v1beta2

import (
	_ "embed"

	corev1beta1 "github.com/flightctl/flightctl/api/core/v1beta1"
	"github.com/flightctl/flightctl/pkg/openapi"
	"github.com/getkin/kin-openapi/openapi3"
)

//go:embed openapi.yaml
var specYAML []byte

// spec is the core v1beta2 OpenAPI document. It is a behavior overlay for the
// Device and Fleet replacement operations and resolves its shared schemas from
// the embedded v1beta1 document.
var spec = openapi.NewDocument(specYAML, map[string][]byte{
	"../v1beta1/openapi.yaml": corev1beta1.SpecYAML(),
})

// SpecYAML returns the raw core v1beta2 OpenAPI document.
func SpecYAML() []byte {
	return spec.Raw()
}

// GetSpec returns the parsed OpenAPI specification for the core v1beta2 API.
func GetSpec() (*openapi3.T, error) {
	return spec.Spec()
}
