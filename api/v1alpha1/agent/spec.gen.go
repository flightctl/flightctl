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
	"EoxuAAA=",
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
