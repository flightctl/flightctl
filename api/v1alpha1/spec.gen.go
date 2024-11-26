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
