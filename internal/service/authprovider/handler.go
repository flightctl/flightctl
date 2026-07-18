package authprovider

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/flightctl/flightctl/internal/auth/provider"
	"github.com/flightctl/flightctl/internal/contextutil"
	"github.com/flightctl/flightctl/internal/domain"
	"github.com/flightctl/flightctl/internal/service/common"
	"github.com/flightctl/flightctl/internal/service/events"
	authproviderstore "github.com/flightctl/flightctl/internal/store/authprovider"
	"github.com/flightctl/flightctl/internal/store/selector"
	"github.com/google/uuid"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
)

type ServiceHandler struct {
	store  authproviderstore.Store
	events events.Service
	log    logrus.FieldLogger
}

// NewServiceHandler creates a new authprovider ServiceHandler instance.
func NewServiceHandler(store authproviderstore.Store, events events.Service, log logrus.FieldLogger) *ServiceHandler {
	return &ServiceHandler{store: store, events: events, log: log}
}

var _ Service = (*ServiceHandler)(nil)

// SanitizeAuthProvider clears managed metadata from an untrusted auth provider document
// (HTTP body).
func SanitizeAuthProvider(authProvider *domain.AuthProvider) {
	if authProvider == nil {
		return
	}
	common.NilOutManagedObjectMetaProperties(&authProvider.Metadata)
}

// CreateAuthProviderFromUntrusted sanitizes an untrusted auth provider document, then creates it.
func CreateAuthProviderFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, authProvider domain.AuthProvider) (*domain.AuthProvider, domain.Status) {
	SanitizeAuthProvider(&authProvider)
	return svc.CreateAuthProvider(ctx, orgId, authProvider)
}

// ReplaceAuthProviderFromUntrusted sanitizes an untrusted auth provider document, then replaces it.
func ReplaceAuthProviderFromUntrusted(ctx context.Context, svc Service, orgId uuid.UUID, name string, authProvider domain.AuthProvider) (*domain.AuthProvider, domain.Status) {
	SanitizeAuthProvider(&authProvider)
	return svc.ReplaceAuthProvider(ctx, orgId, name, authProvider)
}

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
func applyAuthProviderDefaults(spec *domain.AuthProviderSpec) error {
	discriminator, err := spec.Discriminator()
	if err != nil {
		return nil // Not a valid provider, nothing to do
	}

	switch discriminator {
	case string(domain.Oidc):
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

	case string(domain.Oauth2):
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
			introspection, err := provider.InferOAuth2IntrospectionConfig(oauth2Spec)
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
func (h *ServiceHandler) handleSuperAdminAnnotation(ctx context.Context, authProvider *domain.AuthProvider) {
	mappedIdentity, ok := contextutil.GetMappedIdentityFromContext(ctx)
	if !ok || !mappedIdentity.IsSuperAdmin() {
		return
	}

	// Clear user-provided annotations and set our annotation
	authProvider.Metadata.Annotations = lo.ToPtr(map[string]string{
		domain.AuthProviderAnnotationCreatedBySuperAdmin: "true",
	})
}

func (h *ServiceHandler) CreateAuthProvider(ctx context.Context, orgId uuid.UUID, authProvider domain.AuthProvider) (*domain.AuthProvider, domain.Status) {

	// Apply defaults for auth providers (only during creation)
	if err := applyAuthProviderDefaults(&authProvider.Spec); err != nil {
		return nil, domain.StatusBadRequest(sanitizeSchemaError(err))
	}

	if errs := authProvider.Validate(ctx); len(errs) > 0 {
		return nil, domain.StatusBadRequest(sanitizeSchemaError(errors.Join(errs...)))
	}

	h.handleSuperAdminAnnotation(ctx, &authProvider)

	result, err := h.store.Create(ctx, orgId, &authProvider, h.callbackAuthProviderUpdated)
	return result, common.StoreErrorToApiStatus(err, true, domain.AuthProviderKind, authProvider.Metadata.Name)
}

func (h *ServiceHandler) ListAuthProviders(ctx context.Context, orgId uuid.UUID, params domain.ListAuthProvidersParams) (*domain.AuthProviderList, domain.Status) {

	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.List(ctx, orgId, *listParams)
	if err == nil {
		return result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) ListAllAuthProviders(ctx context.Context, params domain.ListAuthProvidersParams) (*domain.AuthProviderList, domain.Status) {

	listParams, status := common.PrepareListParams(params.Continue, params.LabelSelector, params.FieldSelector, params.Limit)
	if status != domain.StatusOK() {
		return nil, status
	}

	result, err := h.store.ListAll(ctx, *listParams)
	if err == nil {
		return result, domain.StatusOK()
	}

	var se *selector.SelectorError

	switch {
	case selector.AsSelectorError(err, &se):
		return nil, domain.StatusBadRequest(se.Error())
	default:
		return nil, domain.StatusInternalServerError(err.Error())
	}
}

func (h *ServiceHandler) GetAuthProvider(ctx context.Context, orgId uuid.UUID, name string) (*domain.AuthProvider, domain.Status) {

	result, err := h.store.Get(ctx, orgId, name)
	return result, common.StoreErrorToApiStatus(err, false, domain.AuthProviderKind, &name)
}

func (h *ServiceHandler) ReplaceAuthProvider(ctx context.Context, orgId uuid.UUID, name string, authProvider domain.AuthProvider) (*domain.AuthProvider, domain.Status) {

	// Validate name early for both create and update paths
	if authProvider.Metadata.Name == nil {
		return nil, domain.StatusBadRequest("metadata.name is required")
	}
	if name != *authProvider.Metadata.Name {
		return nil, domain.StatusBadRequest("resource name specified in metadata does not match name in path")
	}

	// Get the existing resource to perform update validation
	currentObj, err := h.store.Get(ctx, orgId, name)
	if err == nil {
		// Resource exists, validate update
		if errs := authProvider.ValidateUpdate(ctx, currentObj); len(errs) > 0 {
			return nil, domain.StatusBadRequest(sanitizeSchemaError(errors.Join(errs...)))
		}

		// Preserve sensitive data from existing provider if the new one contains masked placeholders
		if preserveErr := authProvider.PreserveSensitiveData(currentObj); preserveErr != nil {
			return nil, domain.StatusInternalServerError(preserveErr.Error())
		}
	} else {
		// Resource doesn't exist, delegate to CreateAuthProvider which handles all creation logic
		return h.CreateAuthProvider(ctx, orgId, authProvider)
	}

	result, created, err := h.store.CreateOrUpdate(ctx, orgId, &authProvider, h.callbackAuthProviderUpdated)
	return result, common.StoreErrorToApiStatus(err, created, domain.AuthProviderKind, &name)
}

func (h *ServiceHandler) PatchAuthProvider(ctx context.Context, orgId uuid.UUID, name string, patch domain.PatchRequest) (*domain.AuthProvider, domain.Status) {

	currentObj, err := h.store.Get(ctx, orgId, name)
	if err != nil {
		return nil, common.StoreErrorToApiStatus(err, false, domain.AuthProviderKind, &name)
	}

	newObj := &domain.AuthProvider{}
	err = common.ApplyJSONPatch(ctx, currentObj, newObj, patch, "/authproviders/"+name)
	if err != nil {
		return nil, domain.StatusBadRequest(sanitizeSchemaError(err))
	}

	// Forbid changing metadata.name via PATCH
	if currentObj.Metadata.Name != nil && newObj.Metadata.Name != nil && *currentObj.Metadata.Name != *newObj.Metadata.Name {
		return nil, domain.StatusBadRequest("metadata.name cannot be changed")
	}

	// Use ValidateUpdate to prevent deletion of required fields
	if errs := newObj.ValidateUpdate(ctx, currentObj); len(errs) > 0 {
		return nil, domain.StatusBadRequest(sanitizeSchemaError(errors.Join(errs...)))
	}

	// Preserve sensitive data from existing provider if the new one contains masked placeholders
	if preserveErr := newObj.PreserveSensitiveData(currentObj); preserveErr != nil {
		return nil, domain.StatusInternalServerError(preserveErr.Error())
	}

	common.NilOutManagedObjectMetaProperties(&newObj.Metadata)
	newObj.Metadata.ResourceVersion = nil

	result, err := h.store.Update(ctx, orgId, newObj, h.callbackAuthProviderUpdated)
	return result, common.StoreErrorToApiStatus(err, false, domain.AuthProviderKind, &name)
}

func (h *ServiceHandler) DeleteAuthProvider(ctx context.Context, orgId uuid.UUID, name string) domain.Status {

	err := h.store.Delete(ctx, orgId, name, h.callbackAuthProviderDeleted)
	return common.StoreErrorToApiStatus(err, false, domain.AuthProviderKind, &name)
}

func (h *ServiceHandler) GetAuthProviderByIssuerAndClientId(ctx context.Context, orgId uuid.UUID, issuer string, clientId string) (*domain.AuthProvider, domain.Status) {

	result, err := h.store.GetAuthProviderByIssuerAndClientId(ctx, orgId, issuer, clientId)
	return result, common.StoreErrorToApiStatus(err, false, domain.AuthProviderKind, &issuer)
}

func (h *ServiceHandler) GetAuthProviderByAuthorizationUrl(ctx context.Context, orgId uuid.UUID, authorizationUrl string) (*domain.AuthProvider, domain.Status) {

	result, err := h.store.GetAuthProviderByAuthorizationUrl(ctx, orgId, authorizationUrl)
	return result, common.StoreErrorToApiStatus(err, false, domain.AuthProviderKind, &authorizationUrl)
}

// GetAuthConfig returns the authentication configuration
// The auth config from the middleware already includes all static and dynamic providers
func (h *ServiceHandler) GetAuthConfig(ctx context.Context, authConfig *domain.AuthConfig) (*domain.AuthConfig, domain.Status) {
	return authConfig, domain.StatusOK()
}

// callbackAuthProviderUpdated is the auth provider-specific callback that handles auth provider update events
func (h *ServiceHandler) callbackAuthProviderUpdated(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	if err != nil {
		status := common.StoreErrorToApiStatus(err, created, domain.AuthProviderKind, &name)
		h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedFailureEvent(ctx, created, domain.AuthProviderKind, name, status, nil))
		return
	}

	// Emit success event for create
	if created {
		h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, created, domain.AuthProviderKind, name, nil, h.log, nil))
	} else {
		// Handle update events
		var oldAuthProvider, newAuthProvider *domain.AuthProvider
		var ok bool
		if oldAuthProvider, newAuthProvider, ok = common.CastResources[domain.AuthProvider](oldResource, newResource); !ok {
			return
		}

		updateDetails := common.ComputeResourceUpdatedDetails(oldAuthProvider.Metadata, newAuthProvider.Metadata)
		// Generate ResourceUpdated event if there are spec changes
		if updateDetails != nil {
			h.events.CreateEvent(ctx, orgId, common.GetResourceCreatedOrUpdatedSuccessEvent(ctx, false, domain.AuthProviderKind, name, updateDetails, h.log, nil))
		}
	}
}

// callbackAuthProviderDeleted is the auth provider-specific callback that handles auth provider deletion events
func (h *ServiceHandler) callbackAuthProviderDeleted(ctx context.Context, resourceKind domain.ResourceKind, orgId uuid.UUID, name string, oldResource, newResource interface{}, created bool, err error) {
	h.events.HandleGenericResourceDeletedEvents(ctx, resourceKind, orgId, name, oldResource, newResource, created, err)
}
