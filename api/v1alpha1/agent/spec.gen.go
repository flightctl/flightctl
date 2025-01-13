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
	"+PjwfwEAAP//uM/eaY13AAA=",
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
