package login

// NilAuth is a provider for when authentication is disabled
type NilAuth struct{}

// NewNilAuth creates a new nil auth provider
func NewNilAuth() *NilAuth {
	return &NilAuth{}
}

// Auth returns empty AuthInfo and no error because authentication is disabled
func (n *NilAuth) Auth() (AuthInfo, error) {
	return AuthInfo{}, nil
}

// Renew returns empty AuthInfo and no error — renewal not applicable when auth is disabled
func (n *NilAuth) Renew(refreshToken string) (AuthInfo, error) {
	return AuthInfo{}, nil
}

// Validate is a no-op when authentication is disabled
func (n *NilAuth) Validate(args ValidateArgs) error {
	return nil
}

// SetInsecureSkipVerify is a no-op when authentication is disabled
func (n *NilAuth) SetInsecureSkipVerify(insecureSkipVerify bool) {
}
