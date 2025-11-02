package auth

import (
	"context"
	"errors"
	"net/http"

	api "github.com/flightctl/flightctl/api/v1alpha1"
)

// NilAuth is a special auth type that does nothing
type NilAuth struct{}

func (NilAuth) CheckPermission(_ context.Context, _ string, _ string) (bool, error) {
	return true, nil
}

func (NilAuth) ValidateToken(_ context.Context, _ string) error {
	return nil
}

func (NilAuth) GetIdentity(_ context.Context, _ string) (Identity, error) {
	return nil, errors.New("nil auth")
}

func (NilAuth) GetAuthConfig() *api.AuthConfig {
	return &api.AuthConfig{}
}

func (NilAuth) GetAuthToken(_ *http.Request) (string, error) {
	return "", errors.New("nil auth")
}
