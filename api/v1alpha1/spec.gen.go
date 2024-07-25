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
	"MtodTUeXny//LwAA//+M6C0pOSABAA==",
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
