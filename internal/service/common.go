package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	jsonpatch "github.com/evanphx/json-patch"
	"github.com/flightctl/flightctl/api/v1alpha1"
	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

const (
	MaxRecordsPerListRequest = 1000
)

func IsInternalRequest(ctx context.Context) bool {
	if internal, ok := ctx.Value(consts.InternalRequestCtxKey).(bool); ok && internal {
		return true
	}
	return false
}

func NilOutManagedObjectMetaProperties(om *v1alpha1.ObjectMeta) {
	if om == nil {
		return
	}
	om.Generation = nil
	om.Owner = nil
	om.Annotations = nil
	om.CreationTimestamp = nil
	om.DeletionTimestamp = nil
}

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

func ApplyJSONPatch[T any](ctx context.Context, obj T, newObj T, patchRequest api.PatchRequest, objPath string) error {
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

func StoreErrorToApiStatus(err error, created bool, kind string, name *string) api.Status {
	if err == nil {
		if created {
			return api.StatusCreated()
		}
		return api.StatusOK()
	}

	badRequestErrors := map[error]bool{
		flterrors.ErrResourceIsNil:                 true,
		flterrors.ErrResourceNameIsNil:             true,
		flterrors.ErrIllegalResourceVersionFormat:  true,
		flterrors.ErrFieldSelectorSyntax:           true,
		flterrors.ErrFieldSelectorParseFailed:      true,
		flterrors.ErrFieldSelectorUnknownSelector:  true,
		flterrors.ErrLabelSelectorSyntax:           true,
		flterrors.ErrLabelSelectorParseFailed:      true,
		flterrors.ErrAnnotationSelectorSyntax:      true,
		flterrors.ErrAnnotationSelectorParseFailed: true,
	}

	conflictErrors := map[error]bool{
		flterrors.ErrUpdatingResourceWithOwnerNotAllowed: true,
		flterrors.ErrDuplicateName:                       true,
		flterrors.ErrNoRowsUpdated:                       true,
		flterrors.ErrResourceVersionConflict:             true,
		flterrors.ErrResourceOwnerIsNil:                  true,
		flterrors.ErrTemplateVersionIsNil:                true,
		flterrors.ErrInvalidTemplateVersion:              true,
		flterrors.ErrNoRenderedVersion:                   true,
		flterrors.ErrDecommission:                        true,
	}

	switch {
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return api.StatusResourceNotFound(kind, util.DefaultIfNil(name, "none"))
	case badRequestErrors[err]:
		return api.StatusBadRequest(err.Error())
	case conflictErrors[err]:
		return api.StatusResourceVersionConflict(err.Error())
	default:
		return api.StatusInternalServerError(err.Error())
	}
}

func ApiStatusToErr(status api.Status) error {
	if status.Code >= 200 && status.Code < 300 {
		return nil
	}
	return errors.New(status.Message)
}
