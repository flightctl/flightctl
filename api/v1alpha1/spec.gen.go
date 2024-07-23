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
	"hg9VHgEA",
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
