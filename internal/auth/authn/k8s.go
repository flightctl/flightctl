package authn

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	api "github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/common"
	"github.com/flightctl/flightctl/internal/identity"
	"github.com/flightctl/flightctl/pkg/k8sclient"
	"github.com/jellydator/ttlcache/v3"
	"github.com/samber/lo"
	"github.com/sirupsen/logrus"
	k8sAuthenticationV1 "k8s.io/api/authentication/v1"
)

const (
	organizationPrefix = "org-"
)

type K8sAuthN struct {
	k8sClient               k8sclient.K8SClient
	externalOpenShiftApiUrl string
	cache                   *ttlcache.Cache[string, *k8sAuthenticationV1.TokenReview]
	rbacNamespace           string
}

func NewK8sAuthN(k8sClient k8sclient.K8SClient, externalOpenShiftApiUrl string, rbacNamespace string) (*K8sAuthN, error) {
	authN := &K8sAuthN{
		k8sClient:               k8sClient,
		externalOpenShiftApiUrl: externalOpenShiftApiUrl,
		cache:                   ttlcache.New(ttlcache.WithTTL[string, *k8sAuthenticationV1.TokenReview](5 * time.Second)),
		rbacNamespace:           rbacNamespace,
	}
	go authN.cache.Start()
	return authN, nil
}

func (o K8sAuthN) loadTokenReview(ctx context.Context, token string) (*k8sAuthenticationV1.TokenReview, error) {
	item := o.cache.Get(token)
	if item != nil {
		return item.Value(), nil
	}
	// Standard TokenReview without audiences; API server validates bound SA tokens
	body, err := json.Marshal(k8sAuthenticationV1.TokenReview{
		Spec: k8sAuthenticationV1.TokenReviewSpec{
			Token: token,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("marshaling resource: %w", err)
	}
	res, err := o.k8sClient.PostCRD(ctx, "authentication.k8s.io/v1/tokenreviews", body)
	if err != nil {
		logrus.WithError(err).Warn("TokenReview request failed")
		return nil, err
	}

	review := &k8sAuthenticationV1.TokenReview{}
	if err := json.Unmarshal(res, review); err != nil {
		logrus.WithError(err).Warn("TokenReview unmarshal failed")
		return nil, err
	}
	// Debug log the TokenReview status (without logging the token)
	logrus.WithFields(logrus.Fields{
		"authenticated": review.Status.Authenticated,
		"user":          review.Status.User.Username,
		"audiences":     review.Status.Audiences,
		"error":         review.Status.Error,
	}).Debug("TokenReview status")
	o.cache.Set(token, review, ttlcache.DefaultTTL)
	return review, nil
}

func (o K8sAuthN) ValidateToken(ctx context.Context, token string) error {
	review, err := o.loadTokenReview(ctx, token)
	if err != nil {
		return err
	}
	if !review.Status.Authenticated {
		return fmt.Errorf("user is not authenticated")
	}
	return nil
}

func (o K8sAuthN) GetAuthToken(r *http.Request) (string, error) {
	return common.ExtractBearerToken(r)
}

func (o K8sAuthN) GetIdentity(ctx context.Context, token string) (common.Identity, error) {
	review, err := o.loadTokenReview(ctx, token)
	if err != nil {
		return nil, err
	}

	var roles []string
	var organizations []string

	// Check if the token has OpenShift-specific scopes claim
	scopes, err := o.parseScopesFromToken(token)
	if err == nil && len(scopes) > 0 {
		// Token has scopes claim - this is an OpenShift OAuth token
		logrus.WithFields(logrus.Fields{
			"user":   review.Status.User.Username,
			"scopes": scopes,
		}).Info("Processing OpenShift OAuth token")
		roles = o.extractRolesFromOpenShiftScopes(token)
		organizations = o.extractOrganizationsFromOpenShiftScopes(token)
	} else {
		// Token doesn't have scopes claim - this is a regular K8s token
		// Get roles from RoleBindings instead of TokenReview groups
		rolesFromBindings, err := o.fetchRoleBindingsForUser(ctx, review.Status.User.Username)
		if err == nil && len(rolesFromBindings) > 0 {
			roles = rolesFromBindings
			logrus.WithFields(logrus.Fields{
				"user":  review.Status.User.Username,
				"roles": roles,
			}).Info("Processing regular K8s token with roles from RoleBindings")
		}
		// Extract organizations from roles for K8s tokens
		organizations = o.extractOrganizationsFromRoles(roles)
	}
	logrus.WithFields(logrus.Fields{
		"user":          review.Status.User.Username,
		"organizations": organizations,
		"roles":         roles,
	}).Info("Extracted organizations and roles")

	// Remove organizations from roles since they are orgs, not roles
	roles = o.removeOrganizationsFromRoles(roles)
	logrus.WithFields(logrus.Fields{
		"user":  review.Status.User.Username,
		"roles": roles,
	}).Info("Filtered roles after removing organizations")

	// Create issuer with K8s cluster information
	issuer := identity.NewIssuer(identity.AuthTypeK8s, "k8s-cluster") // TODO: Get actual cluster name from config
	logrus.WithFields(logrus.Fields{
		"user":          review.Status.User.Username,
		"uid":           review.Status.User.UID,
		"organizations": organizations,
		"roles":         roles,
	}).Info("K8s identity created")
	reportedOrganizations := make([]common.ReportedOrganization, 0, len(organizations))
	for _, org := range organizations {
		reportedOrganizations = append(reportedOrganizations, common.ReportedOrganization{
			Name:         org,
			IsInternalID: false,
			ID:           org,
		})
	}
	return common.NewBaseIdentityWithIssuer(review.Status.User.Username, review.Status.User.UID, reportedOrganizations, roles, issuer), nil
}

// extractOrganizationsFromRoles extracts organization names from K8s groups
// Groups with prefix "org_" are treated as organizations
func (o K8sAuthN) extractOrganizationsFromRoles(roles []string) []string {
	var organizations []string
	for _, role := range roles {
		// Check if role starts with organizationPrefix
		if strings.HasPrefix(role, organizationPrefix) {
			orgName := role[len(organizationPrefix):] // Remove organizationPrefix
			if orgName != "" {
				organizations = append(organizations, orgName)
			}
		}
	}
	return organizations
}

// removeOrganizationsFromRoles removes organization entries from the roles list
// since organizations are not roles
func (o K8sAuthN) removeOrganizationsFromRoles(roles []string) []string {
	var filteredRoles []string
	for _, role := range roles {
		// Keep roles that don't start with organizationPrefix
		if !strings.HasPrefix(role, organizationPrefix) {
			filteredRoles = append(filteredRoles, role)
		}
	}
	return filteredRoles
}

// fetchRoleBindingsForUser fetches RoleBindings for a user in the configured RBAC namespace
func (o K8sAuthN) fetchRoleBindingsForUser(ctx context.Context, username string) ([]string, error) {
	if o.rbacNamespace == "" {
		logrus.Debug("RBAC namespace not configured, skipping RoleBinding lookup")
		return []string{}, nil
	}

	// Fetch all RoleBindings in the namespace
	roleBindingList, err := o.k8sClient.ListRoleBindings(ctx, o.rbacNamespace)
	if err != nil {
		logrus.WithError(err).Warn("Failed to fetch RoleBindings")
		return []string{}, nil // Return empty list instead of error to not break auth flow
	}

	var roleNames []string
	for _, binding := range roleBindingList.Items {
		// Check if the user is a subject in this binding
		for _, subject := range binding.Subjects {
			if subject.Kind == "User" && subject.Name == username {
				roleNames = append(roleNames, binding.RoleRef.Name)
				break
			}
			// Also check for ServiceAccount subjects
			if subject.Kind == "ServiceAccount" && fmt.Sprintf("system:serviceaccount:%s:%s", subject.Namespace, subject.Name) == username {
				roleNames = append(roleNames, binding.RoleRef.Name)
				break
			}
		}
	}

	logrus.WithFields(logrus.Fields{
		"user":      username,
		"namespace": o.rbacNamespace,
		"roles":     roleNames,
	}).Info("Fetched roles from RoleBindings")

	return roleNames, nil
}

func (o K8sAuthN) GetAuthConfig() *api.AuthConfig {
	providerType := string(api.AuthProviderInfoTypeK8s)
	providerName := string(api.AuthProviderInfoTypeK8s)
	provider := api.AuthProviderInfo{
		Name:      &providerName,
		Type:      (*api.AuthProviderInfoType)(&providerType),
		AuthUrl:   &o.externalOpenShiftApiUrl,
		IsDefault: lo.ToPtr(true),
		IsStatic:  lo.ToPtr(true),
	}

	return &api.AuthConfig{
		DefaultProvider:      &providerType,
		OrganizationsEnabled: lo.ToPtr(true),
		Providers:            &[]api.AuthProviderInfo{provider},
	}
}

// extractRolesFromOpenShiftScopes extracts roles from OpenShift OAuth token scopes
// OpenShift OAuth tokens include a "scopes" claim with role information like "user:full", "user:info", etc.
func (o K8sAuthN) extractRolesFromOpenShiftScopes(token string) []string {
	// Parse the JWT token to extract the scopes claim
	scopes, err := o.parseScopesFromToken(token)
	if err != nil {
		// If we can't parse scopes, fall back to empty roles
		return []string{}
	}

	var roles []string
	for _, scope := range scopes {
		// OpenShift scopes like "user:full", "user:info" can be treated as roles
		// We can also map common OpenShift scopes to more meaningful role names
		role := o.mapOpenShiftScopeToRole(scope)
		if role != "" {
			roles = append(roles, role)
		}
	}

	return roles
}

// extractOrganizationsFromOpenShiftScopes extracts organizations from OpenShift OAuth token scopes
// Organization scopes are expected to start with "org-" or "org_"
func (o K8sAuthN) extractOrganizationsFromOpenShiftScopes(token string) []string {
	scopes, err := o.parseScopesFromToken(token)
	if err != nil {
		return []string{}
	}

	var organizations []string
	for _, scope := range scopes {
		var orgName string
		// Check for "org-" prefix
		if strings.HasPrefix(scope, "org-") {
			orgName = scope[4:] // Remove "org-" prefix
		} else if strings.HasPrefix(scope, "org_") {
			orgName = scope[4:] // Remove "org_" prefix
		}

		// If we found an organization scope
		if orgName != "" {
			// Remove trailing scope suffix after ':' if present
			if idx := strings.Index(orgName, ":"); idx != -1 {
				orgName = orgName[:idx]
			}
			// Append non-empty org names
			if orgName != "" {
				organizations = append(organizations, orgName)
			}
		}
	}

	return organizations
}

// parseScopesFromToken parses the JWT token and extracts the scopes claim
func (o K8sAuthN) parseScopesFromToken(token string) ([]string, error) {
	// Split the JWT token into parts
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT token format")
	}

	// Decode the payload (second part)
	payload := parts[1]
	// Add padding if needed
	if len(payload)%4 != 0 {
		payload += strings.Repeat("=", 4-len(payload)%4)
	}

	// Decode base64
	decoded, err := base64.URLEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	// Parse the JSON payload
	var claims map[string]interface{}
	if err := json.Unmarshal(decoded, &claims); err != nil {
		return nil, fmt.Errorf("failed to parse JWT claims: %w", err)
	}

	// Extract scopes claim
	scopesInterface, exists := claims["scopes"]
	if !exists {
		return []string{}, nil
	}

	// Convert to string slice
	var scopes []string
	switch v := scopesInterface.(type) {
	case string:
		// If scopes is a single string, split by space
		scopes = strings.Fields(v)
	case []interface{}:
		// If scopes is an array
		for _, scope := range v {
			if scopeStr, ok := scope.(string); ok {
				scopes = append(scopes, scopeStr)
			}
		}
	}

	return scopes, nil
}

// mapOpenShiftScopeToRole maps OpenShift OAuth scopes to more meaningful role names
func (o K8sAuthN) mapOpenShiftScopeToRole(scope string) string {
	switch scope {
	case "user:full":
		return "admin"
	case "user:info":
		return "user"
	case "user:check-access":
		return "viewer"
	default:
		// For other scopes, use the scope name as the role
		return scope
	}
}
