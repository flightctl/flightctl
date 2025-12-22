package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	api "github.com/flightctl/flightctl/api/v1beta1"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
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

// applyAuthProviderDefaults applies default values to auth provider specs during creation.
// This includes setting UsernameClaim for OIDC and OAuth2 providers, Issuer for OAuth2,
// and inferring Introspection for OAuth2 if not provided.
func applyAuthProviderDefaults(spec *api.AuthProviderSpec) error {
	discriminator, err := spec.Discriminator()
	if err != nil {
		return nil // Not a valid provider, nothing to do
	}

	switch discriminator {
	case string(api.Oidc):
		oidcSpec, err := spec.AsOIDCProviderSpec()
		if err != nil {
			return fmt.Errorf("invalid OIDC provider spec: %w", err)
		}

		// Default UsernameClaim to ["preferred_username"] if not provided
		if oidcSpec.UsernameClaim == nil || len(*oidcSpec.UsernameClaim) == 0 {
			defaultUsernameClaim := []string{"preferred_username"}
			oidcSpec.UsernameClaim = &defaultUsernameClaim
		}

		// Merge the mutated spec back into the union
		if mergeErr := spec.MergeOIDCProviderSpec(oidcSpec); mergeErr != nil {
			return fmt.Errorf("failed to update OIDC provider spec: %w", mergeErr)
		}

	case string(api.Oauth2):
		oauth2Spec, err := spec.AsOAuth2ProviderSpec()
		if err != nil {
			return fmt.Errorf("invalid OAuth2 provider spec: %w", err)
		}

		// Default UsernameClaim to ["preferred_username"] if not provided
		if oauth2Spec.UsernameClaim == nil || len(*oauth2Spec.UsernameClaim) == 0 {
			defaultUsernameClaim := []string{"preferred_username"}
			oauth2Spec.UsernameClaim = &defaultUsernameClaim
		}

		// Use authorizationUrl as issuer if issuer is not provided
		if oauth2Spec.Issuer == nil || *oauth2Spec.Issuer == "" {
			oauth2Spec.Issuer = &oauth2Spec.AuthorizationUrl
		}

		// Infer introspection if not provided
		if oauth2Spec.Introspection == nil {
			introspection, err := api.InferOAuth2IntrospectionConfig(oauth2Spec)
			if err != nil {
				return fmt.Errorf("introspection field is required and could not be inferred: %w", err)
			}
			oauth2Spec.Introspection = introspection
		}

		// Merge the mutated spec back into the union
		if mergeErr := spec.MergeOAuth2ProviderSpec(oauth2Spec); mergeErr != nil {
			return fmt.Errorf("failed to update OAuth2 provider spec: %w", mergeErr)
		}
	}

	return nil
}

// handleSuperAdminAnnotation checks if the request is from a super admin and sets the annotation if needed.
// Returns true if the auth provider was created by a super admin, false otherwise.
func (h *ServiceHandler) handleSuperAdminAnnotation(ctx context.Context, authProvider *api.AuthProvider) bool {
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	createdBySuperAdmin := ok && mappedIdentity.IsSuperAdmin()

	if createdBySuperAdmin {
		// Clear user-provided annotations and set our annotation
		authProvider.Metadata.Annotations = lo.ToPtr(map[string]string{
			api.AuthProviderAnnotationCreatedBySuperAdmin: "true",
		})
	}
	return createdBySuperAdmin
}

func (h *ServiceHandler) CreateAuthProvider(ctx context.Context, orgId uuid.UUID, authProvider api.AuthProvider) (*api.AuthProvider, api.Status) {

	// don't set fields that are managed by the service
	NilOutManagedObjectMetaProperties(&authProvider.Metadata)

	// Apply defaults for auth providers (only during creation)
	if err := applyAuthProviderDefaults(&authProvider.Spec); err != nil {
		return nil, api.StatusBadRequest(sanitizeSchemaError(err))
	}

	if errs := authProvider.Validate(ctx); len(errs) > 0 {
		return nil, api.StatusBadRequest(sanitizeSchemaError(errors.Join(errs...)))
	}

	// Check if created by super admin and prepare annotations
	createdBySuperAdmin := h.handleSuperAdminAnnotation(ctx, &authProvider)

	// Use fromAPI=false to preserve annotations when created by super admin
	if createdBySuperAdmin {
		result, err := h.store.AuthProvider().CreateWithFromAPI(ctx, orgId, &authProvider, false, h.callbackAuthProviderUpdated)
		return result, StoreErrorToApiStatus(err, true, api.AuthProviderKind, authProvider.Metadata.Name)
	}

	// For non-super-admin users, use regular Create (fromAPI=true, annotations cleared)
	result, err := h.store.AuthProvider().Create(ctx, orgId, &authProvider, h.callbackAuthProviderUpdated)
	return result, StoreErrorToApiStatus(err, true, api.AuthProviderKind, authProvider.Metadata.Name)
}

func (h *ServiceHandler) ListAuthProviders(ctx context.Context, orgId uuid.UUID, params api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status) {

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

func (h *ServiceHandler) ListAllAuthProviders(ctx context.Context, params api.ListAuthProvidersParams) (*api.AuthProviderList, api.Status) {

	listParams, status := prepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != api.StatusOK() {
		return nil, status
	}

	result, err := h.store.AuthProvider().ListAll(ctx, *listParams)
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

func (h *ServiceHandler) GetAuthProvider(ctx context.Context, orgId uuid.UUID, name string) (*api.AuthProvider, api.Status) {

	result, err := h.store.AuthProvider().Get(ctx, orgId, name)
	return result, StoreErrorToApiStatus(err, false, api.AuthProviderKind, &name)
}

func (h *ServiceHandler) ReplaceAuthProvider(ctx context.Context, orgId uuid.UUID, name string, authProvider api.AuthProvider) (*api.AuthProvider, api.Status) {

	// don't overwrite fields that are managed by the service for external requests
	if !IsInternalRequest(ctx) {
		NilOutManagedObjectMetaProperties(&authProvider.Metadata)
	}

	// Validate name early for both create and update paths
	if authProvider.Metadata.Name == nil {
		return nil, api.StatusBadRequest("metadata.name is required")
	}
	if name != *authProvider.Metadata.Name {
		return nil, api.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	// Get the existing resource to perform update validation
	currentObj, err := h.store.AuthProvider().Get(ctx, orgId, name)
	if err == nil {
		// Resource exists, validate update
		if errs := authProvider.ValidateUpdate(ctx, currentObj); len(errs) > 0 {
			return nil, api.StatusBadRequest(sanitizeSchemaError(errors.Join(errs...)))
		}
	} else {
		// Resource doesn't exist, delegate to CreateAuthProvider which handles all creation logic
		return h.CreateAuthProvider(ctx, orgId, authProvider)
	}

	result, created, err := h.store.AuthProvider().CreateOrUpdate(ctx, orgId, &authProvider, h.callbackAuthProviderUpdated)
	return result, StoreErrorToApiStatus(err, created, api.AuthProviderKind, &name)
}

func (h *ServiceHandler) PatchAuthProvider(ctx context.Context, orgId uuid.UUID, name string, patch api.PatchRequest) (*api.AuthProvider, api.Status) {

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

	// Use ValidateUpdate to prevent deletion of required fields
	if errs := newObj.ValidateUpdate(ctx, currentObj); len(errs) > 0 {
		return nil, api.StatusBadRequest(sanitizeSchemaError(errors.Join(errs...)))
	}

	NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	result, err := h.store.AuthProvider().Update(ctx, orgId, newObj, h.callbackAuthProviderUpdated)
	return result, StoreErrorToApiStatus(err, false, api.AuthProviderKind, &name)
}

func (h *ServiceHandler) DeleteAuthProvider(ctx context.Context, orgId uuid.UUID, name string) api.Status {

	err := h.store.AuthProvider().Delete(ctx, orgId, name, h.callbackAuthProviderDeleted)
	return StoreErrorToApiStatus(err, false, api.AuthProviderKind, &name)
}

func (h *ServiceHandler) GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*api.AuthProvider, api.Status) {

	result, err := h.store.AuthProvider().GetAuthProviderByIssuerAndClientId(ctx, orgId, issuer, clientId)
	return result, StoreErrorToApiStatus(err, false, api.AuthProviderKind, &issuer)
}

func (h *ServiceHandler) GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*api.AuthProvider, api.Status) {

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
