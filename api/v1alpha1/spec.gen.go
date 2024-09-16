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
