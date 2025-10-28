//go:build linux

package issuer

// Ensure SSSDOIDCProvider implements OIDCIssuer interface
// This check is only compiled when SSSD support is available
var _ OIDCIssuer = (*SSSDOIDCProvider)(nil)
