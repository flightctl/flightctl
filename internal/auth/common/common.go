package common

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/flightctl/flightctl/internal/consts"
)

const (
	AuthHeader string = "Authorization"
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

type Identity interface {
	GetUsername() string
	GetUID() string
	GetGroups() []string
}

type BaseIdentity struct {
	username string
	uID      string
	groups   []string
}

// Ensure BaseIdentity implements Identity
var _ Identity = (*BaseIdentity)(nil)

func NewBaseIdentity(username string, uID string, groups []string) *BaseIdentity {
	return &BaseIdentity{
		username: username,
		uID:      uID,
		groups:   groups,
	}
}

func (i *BaseIdentity) GetUsername() string {
	return i.username
}

func (i *BaseIdentity) SetUsername(username string) {
	i.username = username
}

func (i *BaseIdentity) GetUID() string {
	return i.uID
}

func (i *BaseIdentity) SetUID(uID string) {
	i.uID = uID
}

func (i *BaseIdentity) GetGroups() []string {
	return i.groups
}

func (i *BaseIdentity) SetGroups(groups []string) {
	i.groups = groups
}

func GetIdentity(ctx context.Context) (Identity, error) {
	identityVal := ctx.Value(consts.IdentityCtxKey)
	if identityVal == nil {
		return nil, fmt.Errorf("failed to get identity from context")
	}
	identity, ok := identityVal.(Identity)
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
