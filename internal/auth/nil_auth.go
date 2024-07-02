package auth

import (
	"context"
	"errors"
)

type NilAuth struct{}

func (a NilAuth) ValidateToken(ctx context.Context) (bool, error) {
	return true, nil
}

func (a NilAuth) GetTokenRequestURL(ctx context.Context) (string, error) {
	return "", errors.New("auth disabled")
}

func (NilAuth) CheckPermission(ctx context.Context, resource string, op string) (bool, error) {
	return true, nil
}
