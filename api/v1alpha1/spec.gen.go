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
	"EhzsAAA=",
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
