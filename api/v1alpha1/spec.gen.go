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
	"A037cQEA",
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
