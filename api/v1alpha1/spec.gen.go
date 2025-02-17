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
	"3c3/CwAA///bei++fewBAA==",
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
