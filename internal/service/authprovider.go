package service

import (
	"context"
	"errors"
	"strings"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
)

// sanitizeSchemaError inspects the error and redacts sensitive fields
func sanitizeSchemaError(err error) string {
	if err == nil {
		return ""
	}
	errMsg := err.Error()
	// Check if the error message contains sensitive field names
	sensitiveFields := []string{"clientSecret", "clientsecret", "secret", "password", "token"}
	for _, field := range sensitiveFields {
		if strings.Contains(strings.ToLower(errMsg), field) {
			return "validation failed: sensitive fields redacted"
		}
	}
	return errMsg
}

func (h *ServiceHandler) CreateAuthProvider(ctx context.Context, authProvider api.AuthProvider) (*api.AuthProvider, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't set fields that are managed by the service
	NilOutManagedObjectMetaProperties(&authProvider.Metadata)

	if errs := authProvider.Validate(ctx); len(errs) > 0 {
		return nil, api.StatusBadRequest(sanitizeSchemaError(errors.Join(errs...)))
	}

	result, err := h.store.AuthProvider().Create(ctx, orgId, &authProvider, h.callbackAuthProviderUpdated)
	return result, StoreErrorToApiStatus(err, true, api.AuthProviderKind, authProvider.Metadata.Name)
}

func (h *ServiceHandler) ListAuthProviders(ctx context.Context, params api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}

	result, err := h.store.AuthProvider().List(ctx, orgId, *listParams)
	if err == nil {
		return result, api.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, api.StatusBadRequest(se.Error())
	default:
		return nil, api.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) GetAuthProvider(ctx context.Context, name string) (*api.AuthProvider, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.AuthProvider().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.AuthProviderKind, &name)
}

func (h *ServiceHandler) ReplaceAuthProvider(ctx context.Context, name string, authProvider api.AuthProvider) (*api.AuthProvider, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	// don't overwrite fields that are managed by the service for external requests
	if !IsInternalRequest(ctx) {
		NilOutManagedObjectMetaProperties(&authProvider.Metadata)
	}

	if errs := authProvider.Validate(ctx); len(errs) > 0 {
		return nil, api.StatusBadRequest(sanitizeSchemaError(errors.Join(errs...)))
	}
	if authProvider.Metadata.Name == nil {
		return nil, api.StatusBadRequest("metadata.name is required")
	}
	if name != *authProvider.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	result, created, err := h.store.AuthProvider().CreateOrUpdate(ctx, orgId, &authProvider, h.callbackAuthProviderUpdated)
	return result, StoreErrorToApiStatus(err, created, api.AuthProviderKind, &name)
}

func (h *ServiceHandler) PatchAuthProvider(ctx context.Context, name string, patch api.PatchRequest) (*api.AuthProvider, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	currentObj, err := h.store.AuthProvider().Get(ctx, orgId, name)
	if err != nil {
		return nil, StoreErrorToApiStatus(err, false, api.AuthProviderKind, &name)
	}

	newObj := &api.AuthProvider{}
	err = ApplyJSONPatch(ctx, currentObj, newObj, patch, "/api/v1/authproviders/"+name)
	if err != nil {
		return nil, api.StatusBadRequest(sanitizeSchemaError(err))
	}

	// Forbid changing metadata.name via PATCH
	if currentObj.Metadata.Name != nil && newObj.Metadata.Name != nil && *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return nil, api.StatusBadRequest("metadata.name cannot be changed")
	}

	if errs := newObj.Validate(ctx); len(errs) > 0 {
		return nil, api.StatusBadRequest(sanitizeSchemaError(errors.Join(errs...)))
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	result, err := h.store.AuthProvider().Update(ctx, orgId, newObj, h.callbackAuthProviderUpdated)
	return result, StoreErrorToApiStatus(err, false, api.AuthProviderKind, &name)
}

func (h *ServiceHandler) DeleteAuthProvider(ctx context.Context, name string) api.Status {
	orgId := getOrgIdFromContext(ctx)

	err := h.store.AuthProvider().Delete(ctx, orgId, name, h.callbackAuthProviderDeleted)
	return StoreErrorToApiStatus(err, false, api.AuthProviderKind, &name)
}

func (h *ServiceHandler) GetAuthProviderByIssuerAndClientId(ctx context.Context, issuer string, clientId string) (*api.AuthProvider, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.AuthProvider().GetAuthProviderByIssuerAndClientId(ctx, orgId, issuer, clientId)
	return result, StoreErrorToApiStatus(err, false, api.AuthProviderKind, &issuer)
}

func (h *ServiceHandler) GetAuthProviderByAuthorizationUrl(ctx context.Context, authorizationUrl string) (*api.AuthProvider, api.Status) {
	orgId := getOrgIdFromContext(ctx)

	result, err := h.store.AuthProvider().GetAuthProviderByAuthorizationUrl(ctx, orgId, authorizationUrl)
	return result, StoreErrorToApiStatus(err, false, api.AuthProviderKind, &authorizationUrl)
}

// callbackAuthProviderUpdated is the auth provider-specific callback that handles auth provider update events
func (h *ServiceHandler) callbackAuthProviderUpdated(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleAuthProviderUpdatedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}

// callbackAuthProviderDeleted is the auth provider-specific callback that handles auth provider deletion events
func (h *ServiceHandler) callbackAuthProviderDeleted(ctx context.Context, resourceKind api.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.eventHandler.HandleAuthProviderDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
