package common

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

type ctxKeyAuthHeader string

const (
	AuthHeader     string           = "Authorization"
	TokenCtxKey    ctxKeyAuthHeader = "TokenCtxKey"
	IdentityCtxKey ctxKeyAuthHeader = "IdentityCtxKey"
)

const (
	AuthTypeK8s  = "k8s"
	AuthTypeOIDC = "OIDC"
	AuthTypeAAP  = "AAPGateway"
)

type AuthConfig struct {
	Type                string
	Url                 string
	OrganizationsConfig AuthOrganizationsConfig
}

type AuthOrganizationsConfig struct {
	Enabled bool
}

type Identity struct {
	Username string
	UID      string
	Groups   []string
}

func GetIdentity(ctx context.Context) (*Identity, error) {
	identityVal := ctx.Value(IdentityCtxKey)
	if identityVal == nil {
		return nil, fmt.Errorf("failed to get identity from context")
	}
	identity, ok := identityVal.(*Identity)
	if !ok {
		return nil, fmt.Errorf("incorrect type of identity in context")
	}
	return identity, nil
}

func ExtractBearerToken(r *http.Request) (string, error) {
	authHeader := r.Header.Get(AuthHeader)
	if authHeader == "" {
		return "", fmt.Errorf("empty %s header", AuthHeader)
	}
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == authHeader {
		return "", fmt.Errorf("invalid %s header", AuthHeader)
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return "", fmt.Errorf("invalid token")
	}
	return token, nil
}

// JWT Claims Extraction Utilities

// ExtractUsernameFromJWTClaims extracts username from JWT claims with fallback priority:
// preferred_username -> email -> name -> sub
func ExtractUsernameFromJWTClaims(claims map[string]interface{}) string {
	// Priority 1: preferred_username (standard OIDC claim)
	if username, ok := claims["preferred_username"].(string); ok && username != "" {
		return username
	}

	// Priority 2: email (common fallback)
	if email, ok := claims["email"].(string); ok && email != "" {
		return email
	}

	// Priority 3: name (display name)
	if name, ok := claims["name"].(string); ok && name != "" {
		return name
	}

	// Priority 4: sub (subject - guaranteed to exist in valid JWT)
	if sub, ok := claims["sub"].(string); ok && sub != "" {
		return sub
	}

	return ""
}

// ExtractUIDFromJWTClaims extracts user ID from JWT claims (typically 'sub')
func ExtractUIDFromJWTClaims(claims map[string]interface{}) string {
	if sub, ok := claims["sub"].(string); ok {
		return sub
	}
	return ""
}

// ExtractGroupsFromJWTClaims extracts groups from JWT claims supporting multiple formats:
// - groups as []interface{} (standard)
// - groups as string (space-separated)
// - roles as []interface{} (alternative)
func ExtractGroupsFromJWTClaims(claims map[string]interface{}) []string {
	var groups []string

	// Try 'groups' claim first (most common)
	if groupClaim, exists := claims["groups"]; exists {
		groups = append(groups, parseJWTGroupClaim(groupClaim)...)
	}

	// Try 'roles' claim as alternative (Azure AD, etc.)
	if rolesClaim, exists := claims["roles"]; exists {
		groups = append(groups, parseJWTGroupClaim(rolesClaim)...)
	}

	// Remove duplicates while preserving order
	result := RemoveDuplicateStrings(groups)

	// Ensure we return empty slice instead of nil
	if result == nil {
		return []string{}
	}
	return result
}

// parseJWTGroupClaim handles different group claim formats from JWT tokens
func parseJWTGroupClaim(claim interface{}) []string {
	var groups []string

	switch v := claim.(type) {
	case []interface{}:
		// Array format: ["group1", "group2", ...]
		for _, item := range v {
			if str, ok := item.(string); ok && str != "" {
				groups = append(groups, str)
			}
		}
	case string:
		// Space-separated string format: "group1 group2 group3"
		if v != "" {
			parts := strings.Fields(v)
			for _, part := range parts {
				if part != "" {
					groups = append(groups, part)
				}
			}
		}
	}

	return groups
}

// RemoveDuplicateStrings removes duplicate strings while preserving order
func RemoveDuplicateStrings(input []string) []string {
	seen := make(map[string]bool)
	var result []string

	for _, item := range input {
		if !seen[item] && item != "" {
			seen[item] = true
			result = append(result, item)
		}
	}

	return result
}
