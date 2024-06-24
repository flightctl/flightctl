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
	"5Qo5AAA=",
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
