package common

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"sync"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/flterrors"
	"github.com/flightctl/flightctl/internal/store"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/flightctl/flightctl/internal/util"
	"github.com/flightctl/flightctl/pkg/jsonpatch"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
	"github.com/samber/lo"
)

// MaxRecordsPerListRequest bounds the number of records a single list request may return.
const MaxRecordsPerListRequest = 1000

// MaxConcurrentAgents bounds the number of concurrent agent-originated requests a resource's
// ServiceHandler will process at once via its own semaphore.Weighted gate.
const MaxConcurrentAgents = 15

// NilOutManagedObjectMetaProperties clears the ObjectMeta fields that are managed by the
// service and must not be set by API callers.
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

// ApplyJSONPatch applies patchRequest to obj, validates the result against the OpenAPI
// schema at objPath, and decodes it into newObj.
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

// StoreErrorToApiStatus maps a store error into the appropriate domain.Status.
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

// ApiStatusToErr converts a non-2xx domain.Status into an error, or nil for 2xx statuses.
func ApiStatusToErr(status domain.Status) error {
	if status.Code >= 200 && status.Code < 300 {
		return nil
	}
	return errors.New(status.Message)
}

// HasConditionChanged checks if a condition actually changed between old and new.
func HasConditionChanged(oldCondition, newCondition *domain.Condition) bool {
	if oldCondition == nil && newCondition == nil {
		return false
	}
	if oldCondition == nil || newCondition == nil {
		return true
	}

	return oldCondition.Status != newCondition.Status ||
		oldCondition.Reason != newCondition.Reason ||
		oldCondition.Message != newCondition.Message
}

// PrepareListParams parses list-request query parameters into store.ListParams, applying
// the default/max limit and parsing the continue token and selectors.
//
// Relocated from the unexported internal/service.prepareListParams (renamed/exported as part
// of this move so it is callable from outside internal/service).
func PrepareListParams(cont *string, lSelector *string, fSelector *string, limit *int32) (*store.ListParams, domain.Status) {
	cnt, err := store.ParseContinueString(cont)
	if err != nil {
		return nil, domain.StatusBadRequest(fmt.Sprintf("failed to parse continue parameter: %v", err))
	}

	var fieldSelector *selector.FieldSelector
	if fSelector != nil {
		if fieldSelector, err = selector.NewFieldSelector(*fSelector); err != nil {
			return nil, domain.StatusBadRequest(fmt.Sprintf("failed to parse field selector: %v", err))
		}
	}

	var labelSelector *selector.LabelSelector
	if lSelector != nil {
		if labelSelector, err = selector.NewLabelSelector(*lSelector); err != nil {
			return nil, domain.StatusBadRequest(fmt.Sprintf("failed to parse label selector: %v", err))
		}
	}

	listParams := &store.ListParams{
		Limit:         int(lo.FromPtr(limit)),
		Continue:      cnt,
		FieldSelector: fieldSelector,
		LabelSelector: labelSelector,
	}
	if listParams.Limit == 0 {
		listParams.Limit = MaxRecordsPerListRequest
	} else if listParams.Limit > MaxRecordsPerListRequest {
		return nil, domain.StatusBadRequest(fmt.Sprintf("limit cannot exceed %d", MaxRecordsPerListRequest))
	} else if listParams.Limit < 0 {
		return nil, domain.StatusBadRequest("limit cannot be negative")
	}

	return listParams, domain.StatusOK()
}
