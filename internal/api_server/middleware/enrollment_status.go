package middleware

import (
	"fmt"

	"github.com/flightctl/flightctl/pkg/openapi"
	"github.com/getkin/kin-openapi/openapi3"
)

// EnrollmentStatusRequestSpec loads an isolated validation graph that accepts
// the two server-owned enrollment fields for sanitization and preservation.
func EnrollmentStatusRequestSpec(document []byte, externalRefs map[string][]byte) (*openapi3.T, error) {
	spec, err := openapi.NewDocument(document, externalRefs).Spec()
	if err != nil {
		return nil, err
	}
	for path, property := range map[string]string{
		"/enrollmentrequests":         "enrollmentCertificateName",
		"/certificatesigningrequests": "issuedAt",
	} {
		item := spec.Paths.Find(path)
		if item == nil || item.Post == nil || item.Post.RequestBody == nil || item.Post.RequestBody.Value == nil {
			return nil, fmt.Errorf("missing enrollment request schema at %s", path)
		}
		content := item.Post.RequestBody.Value.Content["application/json"]
		if content == nil || content.Schema == nil || content.Schema.Value == nil {
			return nil, fmt.Errorf("missing enrollment JSON schema at %s", path)
		}
		status := content.Schema.Value.Properties["status"]
		if status == nil || status.Value == nil || status.Value.Properties[property] == nil || status.Value.Properties[property].Value == nil {
			return nil, fmt.Errorf("missing enrollment status property %s at %s", property, path)
		}
		status.Value.Properties[property].Value.ReadOnly = false
	}
	return spec, nil
}
