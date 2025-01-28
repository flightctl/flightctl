// Package v1alpha1 provides primitives to interact with the openapi HTTP API.
//
// Code generated by github.com/oapi-codegen/oapi-codegen/v2 version v2.3.0 DO NOT EDIT.
package v1alpha1

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"fmt"
	"net/url"
	"path"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Base64 encoded, gzipped, json marshaled Swagger object
var swaggerSpec = []string{

	"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",
	"AAAA///MWfCPNO4BAA==",
}

// GetSwagger returns the content of the embedded swagger specification file
// or error if failed to decode
func decodeSpec() ([]byte, error) {
	zipped, err := base64.StdEncoding.DecodeString(strings.Join(swaggerSpec, ""))
	if err != nil {
		return nil, fmt.Errorf("error base64 decoding spec: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(zipped))
	if err != nil {
		return nil, fmt.Errorf("error decompressing spec: %w", err)
	}
	var buf bytes.Buffer
	_, err = buf.ReadFrom(zr)
	if err != nil {
		return nil, fmt.Errorf("error decompressing spec: %w", err)
	}

	return buf.Bytes(), nil
}

var rawSpec = decodeSpecCached()

// a naive cached of a decoded swagger spec
func decodeSpecCached() func() ([]byte, error) {
	data, err := decodeSpec()
	return func() ([]byte, error) {
		return data, err
	}
}

// Constructs a synthetic filesystem for resolving external references when loading openapi specifications.
func PathToRawSpec(pathToFile string) map[string]func() ([]byte, error) {
	res := make(map[string]func() ([]byte, error))
	if len(pathToFile) > 0 {
		res[pathToFile] = rawSpec
	}

	return res
}

// GetSwagger returns the Swagger specification corresponding to the generated code
// in this file. The external references of Swagger specification are resolved.
// The logic of resolving external references is tightly connected to "import-mapping" feature.
// Externally referenced files must be embedded in the corresponding golang packages.
// Urls can be supported but this task was out of the scope.
func GetSwagger() (swagger *openapi3.T, err error) {
	resolvePath := PathToRawSpec("")

	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	loader.ReadFromURIFunc = func(loader *openapi3.Loader, url *url.URL) ([]byte, error) {
		pathToFile := url.String()
		pathToFile = path.Clean(pathToFile)
		getSpec, ok := resolvePath[pathToFile]
		if !ok {
			err1 := fmt.Errorf("path not found: %s", pathToFile)
			return nil, err1
		}
		return getSpec()
	}
	var specData []byte
	specData, err = rawSpec()
	if err != nil {
		return
	}
	swagger, err = loader.LoadFromData(specData)
	if err != nil {
		return
	}
	return
}
