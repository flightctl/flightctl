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

// ExtractBearerToken extracts the bearer token from the Authorization header of r.
// It returns the token string or an error if the Authorization header is missing,
// does not start with the "Bearer " prefix, or contains an empty token after trimming.
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
// ExtractUsernameFromJWTClaims returns a username derived from JWT claims.
// It selects the first non-empty value in this priority: `preferred_username`, `email`, `name`, then `sub`.
// If none of those claims contain a non-empty string, it returns the empty string.
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

// ExtractUIDFromJWTClaims extracts the user identifier from JWT claims.
// It returns the "sub" claim as a string when present and a string type; otherwise it returns an empty string.
func ExtractUIDFromJWTClaims(claims map[string]interface{}) string {
	if sub, ok := claims["sub"].(string); ok {
		return sub
	}
	return ""
}

// ExtractGroupsFromJWTClaims extracts groups from JWT claims supporting multiple formats:
// - groups as []interface{} (standard)
// - groups as string (space-separated)
// ExtractGroupsFromJWTClaims extracts group membership from JWT claims.
// It looks for a "groups" claim first and falls back to "roles" if present.
// Supported claim formats are slices (e.g. []interface{}) and space-separated strings;
// values are parsed, de-duplicated while preserving order, and returned as a
// []string. If no groups are found the function returns an empty slice (not nil).
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

// parseJWTGroupClaim extracts group names from a JWT `groups`/`roles` claim.
// It accepts either an array claim ([]interface{}) where string elements are
// collected, or a space-separated string claim. Non-string items and empty
// strings are ignored. Returns a slice of groups in the order encountered (may
// be empty for unsupported formats or when no valid groups are present).
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

// RemoveDuplicateStrings returns a new slice that preserves the original order
// but omits duplicate and empty strings.
//
// The first occurrence of each non-empty string is kept; subsequent duplicates
// are removed. The input slice is not modified.
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
