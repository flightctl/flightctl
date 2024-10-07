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
