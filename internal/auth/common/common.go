package common

import (
	"context"
	"fmt"
)

type ctxKeyAuthHeader string

const (
	AuthHeader            string           = "Authorization"
	TokenCtxKey           ctxKeyAuthHeader = "TokenCtxKey"
	IdentityCtxKey        ctxKeyAuthHeader = "IdentityCtxKey"
	InternalRequestCtxKey ctxKeyAuthHeader = "InternalRequestCtxKey"
)

type AuthConfig struct {
	Type string
	Url  string
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
