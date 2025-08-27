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
