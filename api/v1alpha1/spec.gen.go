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
	"f/P25v8HAAD//21ESQ3b+AEA",
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
