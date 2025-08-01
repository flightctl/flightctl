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
	"oZn1zQAK2H1qkgYAs8uLV0IqdGcqSoQWZE67h4PqunNv86BWsRe9klLOdRnigmwF4zzGa0MHYnP482k5",
	"du1nzwC/dyvcLXYoJCODrFeIc0ONDy8hZSQ7oWBz/bUdYVNShY5YG+D0IrjtogsDb3PnKZFOiaMnp/LJ",
	"qbwpLO3mSI5d7tZ5HMaMS1rlp7qkhTl4J0z+7JJWdRCjJC2k2ZOk9aVKWj3al4dJ8htTaMY08lwfbzYs",
	"5VF3PtffXjpq2GpbKpP9LJeYyxsSH/tRYrMYtskt4vempAkFxPN6h0pMdP5hLnh0h9TczSXUtz7iDKMO",
	"nO02u/luuhQQVThsQ/BE50ewBMWBBifgg2+rnJGXzIfZsrQBrN7o2uohjG+1xykZtjLoiOy2Oi7542mt",
	"MbgNZcy71vQiNL1k2ZlvHFzADgxzXxuZ1dt5GxqZAh6kNHEzXUuceWgmXvD77bzYvTf61ld5dJJMaD0n",
	"DARSmmVbwqt0GQFqrOk1A4EZvM8g+xXI9VTQFav5fnFBKLlZdylwdnMwLk/803NMpq0sMbsUFpl7jBnF",
	"UdSp1Y4ezd8yYaX/U5bLUjaO+uQvaaZZqxCoe7/6DRo4tLceFaojj8rjXIKRegup5A17Akm30Lo+rlaB",
	"Hdq1ie6Vm0hK6NbTvOLm1G4ntkbl/avQK/NbburJkAm6De6SmsMn5PDlK/jKU6DKuaCjzrb/PPy0VkOV",
	"nGJ0TKTjp+yadz/Zyn21iy40q1jI3vW2yhKXi2/NOu9KMjLvqIne1jTXMoYPr8YV3HYnH5sY6sYlXrdV",
	"qY8aGcGWvclvsfJNoYF73jATyUhxyQj7wJLCYCjKKEpk19ZLjQzfMEdNfmfpMsgj/aieLePR5lE9W4YV",
	"Ih+tH316xoyPscw842zd1e04LcTs43tQ0dV/jKSwuP6Rqk+JgnoprrmSAh7Ea6o4ePVdse1erXQ1F/+D",
	"CSR9GpZCWBhHs1ypQnTaiDYW0PUbGmYOpGJLqFoVG+AcCg1ZXw0VKVUpZkAneisM/WAvjxU8OctSryPX",
	"ZOPM7X4mTXKeQ7HtFYSmz+2N4oDeW3LDVLUIUoiUKULJJdVrspeg2eVDPGrtRqqrF7xDa24/Yoolnyyp",
	"Kt/tMhAVQnix2y10BKkrRCdJqdD2YJe7VnY7oWb9Nh/UN9f6vPxgGTwsGTC4rqBxW6ckCCs/B8SN2fsH",
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
