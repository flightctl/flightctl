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

	externalRef0 "github.com/flightctl/flightctl/api/v1alpha1"
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

	pathPrefix := path.Dir(pathToFile)

	for rawPath, rawFunc := range externalRef0.PathToRawSpec(path.Join(pathPrefix, "../openapi.yaml")) {
		if _, ok := res[rawPath]; ok {
			// it is not possible to compare functions in golang, so always overwrite the old value
		}
		res[rawPath] = rawFunc
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
