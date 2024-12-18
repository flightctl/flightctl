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
