package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

var (
	ErrorInvalidFieldKey   = errors.New("invalid field filter key")
	ErrorInvalidFieldValue = errors.New("invalid field filter value")
)

func validateAgainstSchema(ctx context.Context, obj []byte, objPath string) error {
	swagger, err := v1alpha1.GetSwagger()
	if err != nil {
		return err
	}
	// Skip server name validation
	swagger.Servers = nil

	url, err := url.Parse(objPath)
	if err != nil {
		return err
	}
	httpReq := &http.Request{
		Method: "PUT",
		URL:    url,
		Body:   io.NopCloser(bytes.NewReader(obj)),
		Header: http.Header{"Content-Type": []string{"application/json"}},
	}

	router, err := gorillamux.NewRouter(swagger)
	if err != nil {
		return err
	}
	route, pathParams, err := router.FindRoute(httpReq)
	if err != nil {
		return err
	}

	requestValidationInput := &openapi3filter.RequestValidationInput{
		Request:    httpReq,
		PathParams: pathParams,
		Route:      route,
	}
	return openapi3filter.ValidateRequest(ctx, requestValidationInput)
}

func ApplyJSONPatch[T any](ctx context.Context, obj T, newObj T, patchRequest v1alpha1.PatchRequest, objPath string) error {
	patch, err := json.Marshal(patchRequest)
	if err != nil {
		return err
	}
	jsonPatch, err := jsonpatch.DecodePatch(patch)
	if err != nil {
		return err
	}

	objJSON, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	newJSON, err := jsonPatch.Apply(objJSON)
	if err != nil {
		return err
	}

	//validate the new object against OpenAPI schema
	err = validateAgainstSchema(ctx, newJSON, objPath)
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(newJSON))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&newObj)
}

// ConvertFieldFilterParamsToMap converts filter query params to to a validated filterMap map.
func ConvertFieldFilterParamsToMap(params []string) (map[string][]string, error) {
	fieldMap := make(map[string][]string)
	if len(params) == 0 {
		return fieldMap, nil
	}

	for _, selectorStr := range params {
		selectorStr = strings.TrimSpace(selectorStr)
		if selectorStr == "" {
			continue
		}

		pairs := strings.Split(selectorStr, "=")
		if len(pairs) == 1 {
			return nil, fmt.Errorf("invalid selector format: %s", selectorStr)
		}

		key, err := validateFieldKey(pairs[0])
		if err != nil {
			return nil, err
		}
		value, err := validateFieldValue(pairs[1])
		if err != nil {
			return nil, err
		}
		fieldMap[key] = append(fieldMap[key], value)
	}

	return fieldMap, nil
}

// validateSortField validates a sort field and returns a slice of SortField and a boolean indicating success.
func validateSortField(field string) ([]store.SortField, bool) {
	if field == "" {
		return nil, false
	}

	sortFields := strings.Split(field, ",")
	res := make([]store.SortField, 0, len(sortFields))

	for _, sortField := range sortFields {
		parts := strings.Split(sortField, ":")
		if len(parts) != 2 {
			return nil, false
		}

		if !strings.EqualFold(parts[1], string(store.SortAsc)) &&
			!strings.EqualFold(parts[1], string(store.SortDesc)) {
			return nil, false
		}

		sortOrder := store.SortAsc
		if strings.EqualFold(parts[1], string(store.SortDesc)) {
			sortOrder = store.SortDesc
		}

		res = append(res, store.SortField{
			FieldName: selector.SelectorFieldName(parts[0]),
			Order:     sortOrder,
		})
	}

	return res, true
}

// validateFieldKey validates a field key. Valid characters are [a-zA-Z.]
func validateFieldKey(key string) (string, error) {
	key = strings.TrimSpace(key)
	for _, char := range key {
		if !unicode.IsLetter(char) && char != '.' {
			return "", fmt.Errorf("%w: %s", ErrorInvalidFieldKey, key)
		}
	}
	return key, nil
}

// validateFieldValue validates a field value. Valid characters are [a-zA-Z0-9,-.]
func validateFieldValue(value string) (string, error) {
	value = strings.TrimSpace(value)
	for _, char := range value {
		if !unicode.IsLetter(char) && !unicode.IsDigit(char) && char != ',' && char != '-' && char != '.' {
			return "", fmt.Errorf("%w: %s", ErrorInvalidFieldValue, value)
		}
	}
	return value, nil
}
