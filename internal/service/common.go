package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sync"

	"github.com/flightctl/flightctl/internal/consts"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/jsonpatch"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

const (
	MaxRecordsPerListRequest = 1000
	MaxConcurrentAgents      = 15
)

func IsInternalRequest(ctx context.Context) bool {
	if internal, ok := ctx.Value(consts.InternalRequestCtxKey).(bool); ok && internal {
		return true
	}
	return false
}

func IsResourceSyncRequest(ctx context.Context) bool {
	if rs, ok := ctx.Value(consts.ResourceSyncRequestCtxKey).(bool); ok && rs {
		return true
	}
	return false
}

func NilOutManagedObjectMetaProperties(om *domain.ObjectMeta) {
	if om == nil {
		return
	}
	om.Generation = nil
	om.Owner = nil
	om.Annotations = nil
	om.CreationTimestamp = nil
	om.DeletionTimestamp = nil
}

func NilOutManagedCatalogItemMetaProperties(om *domain.CatalogItemMeta) {
	if om == nil {
		return
	}
	om.Generation = nil
	om.Owner = nil
	om.Annotations = nil
	om.CreationTimestamp = nil
	om.DeletionTimestamp = nil
}

// SwaggerGetter is a function that returns a parsed OpenAPI spec.
type SwaggerGetter func() (*openapi3.T, error)

// cachedRouter holds a lazily-built gorillamux router for a given swagger spec.
type cachedRouter struct {
	once   sync.Once
	router routers.Router
	err    error
}

// routerCache maps swagger getter function pointers to their cached router.
var routerCache sync.Map // key: uintptr, value: *cachedRouter

// getRouterFor returns a cached router for the given swagger getter, building it on first use.
// The router is built with swagger.Servers = nil to skip server name validation.
func getRouterFor(getSwagger SwaggerGetter) (routers.Router, error) {
	ptr := reflect.ValueOf(getSwagger).Pointer()
	entry, _ := routerCache.LoadOrStore(ptr, &cachedRouter{})
	cr := entry.(*cachedRouter)
	cr.once.Do(func() {
		swagger, err := getSwagger()
		if err != nil {
			cr.err = err
			return
		}
		swagger.Servers = nil
		cr.router, cr.err = gorillamux.NewRouter(swagger)
	})
	return cr.router, cr.err
}

func validateAgainstSchema(ctx context.Context, obj []byte, objPath string, getSwagger SwaggerGetter) error {
	router, err := getRouterFor(getSwagger)
	if err != nil {
		return err
	}

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

	route, pathParams, err := router.FindRoute(httpReq)
	if err != nil {
		return err
	}

	requestValidationInput := &openapi3filter.RequestValidationInput{
		Request:    httpReq,
		PathParams: pathParams,
		Route:      route,
		Options:    &openapi3filter.Options{ExcludeReadOnlyValidations: true},
	}
	return openapi3filter.ValidateRequest(ctx, requestValidationInput)
}

func ApplyJSONPatch[T any](ctx context.Context, obj T, newObj T, patchRequest domain.PatchRequest, objPath string, getSwagger ...SwaggerGetter) error {
	if err := jsonpatch.Apply(obj, &newObj, patchRequest); err != nil {
		return err
	}

	newJSON, err := json.Marshal(newObj)
	if err != nil {
		return err
	}

	swaggerFn := SwaggerGetter(domain.GetSwagger)
	if len(getSwagger) > 0 && getSwagger[0] != nil {
		swaggerFn = getSwagger[0]
	}
	err = validateAgainstSchema(ctx, newJSON, objPath, swaggerFn)
	if err != nil {
		return err
	}

	decoder := json.NewDecoder(bytes.NewReader(newJSON))
	decoder.DisallowUnknownFields()
	return decoder.Decode(&newObj)
}

var badRequestErrors = map[error]bool{
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
	flterrors.ErrUnsupportedUnicode:            true,
}

var conflictErrors = map[error]bool{
	flterrors.ErrUpdatingResourceWithOwnerNotAllowed: true,
	flterrors.ErrDuplicateName:                       true,
	flterrors.ErrDuplicateOIDCProvider:               true,
	flterrors.ErrDuplicateOAuth2Provider:             true,
	flterrors.ErrNoRowsUpdated:                       true,
	flterrors.ErrResourceVersionConflict:             true,
	flterrors.ErrResourceOwnerIsNil:                  true,
	flterrors.ErrTemplateVersionIsNil:                true,
	flterrors.ErrInvalidTemplateVersion:              true,
	flterrors.ErrNoRenderedVersion:                   true,
	flterrors.ErrDecommission:                        true,
	flterrors.ErrResourceNotEmpty:                    true,
}

func StoreErrorToApiStatus(err error, created bool, kind string, name *string) domain.Status {
	if err == nil {
		if created {
			return domain.StatusCreated()
		}
		return domain.StatusOK()
	}

	switch {
	case errors.Is(err, flterrors.ErrResourceNotFound):
		return domain.StatusResourceNotFound(kind, util.DefaultIfNil(name, "none"))
	case badRequestErrors[err]:
		return domain.StatusBadRequest(err.Error())
	case conflictErrors[err]:
		return domain.StatusResourceVersionConflict(err.Error())
	default:
		return domain.StatusInternalServerError(err.Error())
	}
}

func ApiStatusToErr(status domain.Status) error {
	if status.Code >= 200 && status.Code < 300 {
		return nil
	}
	return errors.New(status.Message)
}
