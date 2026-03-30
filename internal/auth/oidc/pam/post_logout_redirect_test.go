//go:build linux

package pam

import (
	"testing"

	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
)

func TestPostLogoutRedirectAllowed(t *testing.T) {
	t.Parallel()

	cfg := ca.NewDefault(t.TempDir())
	caClient, _, err := fccrypto.EnsureCA(cfg)
	if err != nil {
		t.Fatalf("EnsureCA: %v", err)
	}
	mockAuth := &MockAuthenticator{}

	pamCfg := &config.PAMOIDCIssuer{
		Issuer:       "https://issuer.example.com",
		ClientID:     "c1",
		RedirectURIs: []string{"https://app.example.com/ui/callback"},
		PAMService:   "test",
	}
	p, err := NewPAMOIDCProviderWithAuthenticator(caClient, pamCfg, mockAuth)
	if err != nil {
		t.Fatalf("NewPAMOIDCProviderWithAuthenticator: %v", err)
	}
	defer p.Close()

	if !p.PostLogoutRedirectAllowed("https://app.example.com/ui/callback") {
		t.Fatal("exact redirect URI should be allowed")
	}
	if !p.PostLogoutRedirectAllowed("https://app.example.com/ui") {
		t.Fatal("UI base (without /callback) should be allowed")
	}
	if p.PostLogoutRedirectAllowed("https://evil.example.com/ui") {
		t.Fatal("unregistered origin should be rejected")
	}
	if p.PostLogoutRedirectAllowed("") {
		t.Fatal("empty URI should be rejected")
	}
}
