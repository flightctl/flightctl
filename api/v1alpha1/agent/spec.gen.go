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
	"4aP6DN3HzJ+sDfzD7cP/BAAA//+qQGC+MIQAAA==",
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
