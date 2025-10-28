//go:build linux

package issuer

// Ensure PAMOIDCProvider implements OIDCIssuer interface
// This check is only compiled when PAM support is available (Linux systems)
var _ OIDCIssuer = (*PAMOIDCProvider)(nil)
