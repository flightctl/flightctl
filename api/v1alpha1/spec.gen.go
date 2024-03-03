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
