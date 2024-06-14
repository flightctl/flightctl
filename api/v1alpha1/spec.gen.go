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
	"/+FFywQH/AAA",
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
