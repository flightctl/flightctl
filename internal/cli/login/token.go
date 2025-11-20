package login

import (
	"fmt"
)

// TokenAuth is a provider for direct token-based authentication
type TokenAuth struct {
	Token string
}

// NewTokenAuth creates a new token-based auth provider
func NewTokenAuth(token string) *TokenAuth {
	return &TokenAuth{
		Token: token,
	}
}

func (t *TokenAuth) SetInsecureSkipVerify(insecureSkipVerify bool) {
}

// Auth returns the pre-configured token
func (t *TokenAuth) Auth() (AuthInfo, error) {
	return AuthInfo{
		AccessToken: t.Token,
		TokenToUse:  TokenToUseAccessToken,
	}, nil
}

// Renew is not supported for token-based authentication
func (t *TokenAuth) Renew(refreshToken string) (AuthInfo, error) {
	return AuthInfo{}, fmt.Errorf("token renewal is not supported for direct token authentication")
}

// Validate performs no validation - token is already provided
func (t *TokenAuth) Validate(args ValidateArgs) error {
	return nil
}
