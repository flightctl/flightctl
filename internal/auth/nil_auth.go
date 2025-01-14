package auth

import (
	"context"

	"github.com/flightctl/flightctl/internal/auth/common"
)

type NilAuth struct{}

func (a NilAuth) ValidateToken(ctx context.Context, token string) (bool, error) {
	return true, nil
}

func (a NilAuth) GetIdentity(ctx context.Context, token string) (*common.Identity, error) {
	return &common.Identity{}, nil
}

func (a NilAuth) GetAuthConfig() common.AuthConfig {
	return common.AuthConfig{
		Type: "",
		Url:  "",
	}
}

func (NilAuth) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	return true, nil
}
