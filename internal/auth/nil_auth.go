package auth

import (
	"context"
	"errors"
)

var ErrAuthDisabled = errors.New("auth disabled")

type NilAuth struct{}

func (a NilAuth) ValidateToken(ctx context.Context, token string) (bool, error) {
	return true, nil
}

func (a NilAuth) GetTokenRequestURL(ctx context.Context) (string, error) {
	return "", ErrAuthDisabled
}

func (NilAuth) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	return true, nil
}
