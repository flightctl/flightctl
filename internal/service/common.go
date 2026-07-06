package service

import (
	"context"

	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
)

// NilOutManagedCatalogItemMetaProperties clears the CatalogItemMeta fields that are managed
// by the service and must not be set by API callers. Catalog-specific; not relocated to
// internal/service/common (no story currently needs it moved).
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

// The functions below are thin forwarding wrappers over internal/service/common, kept here
// with identical signatures so the untouched per-resource files in this package (which call
// these unqualified, same-package style) and external callers in other packages keep
// compiling unchanged. See internal/service/common/http.go for the real implementations.

func IsInternalRequest(ctx context.Context) bool {
	return common.IsInternalRequest(ctx)
}

func IsResourceSyncRequest(ctx context.Context) bool {
	return common.IsResourceSyncRequest(ctx)
}

func NilOutManagedObjectMetaProperties(om *domain.ObjectMeta) {
	common.NilOutManagedObjectMetaProperties(om)
}

func ApplyJSONPatch[T any](ctx context.Context, obj T, newObj T, patchRequest domain.PatchRequest, objPath string, getSwagger ...common.SwaggerGetter) error {
	return common.ApplyJSONPatch(ctx, obj, newObj, patchRequest, objPath, getSwagger...)
}

func StoreErrorToApiStatus(err error, created bool, kind string, name *string) domain.Status {
	return common.StoreErrorToApiStatus(err, created, kind, name)
}

func ApiStatusToErr(status domain.Status) error {
	return common.ApiStatusToErr(status)
}
