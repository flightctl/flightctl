// Package v1alpha1 provides primitives to interact with the openapi HTTP API.
//
// Code generated by github.com/deepmap/oapi-codegen version v1.15.0 DO NOT EDIT.
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
