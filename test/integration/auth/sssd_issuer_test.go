//go:build linux

package auth_test

import (
	"context"
	"testing"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/issuer"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

func TestSSSDIssuerIntegration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SSSD Issuer Integration Suite")
}

var _ = Describe("SSSD Issuer Integration Tests", func() {
	var (
		ctx      context.Context
		provider *issuer.SSSDOIDCProvider
		caClient *fccrypto.CAClient
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Create test CA client
		cfg := ca.NewDefault(GinkgoT().TempDir())
		var err error
		caClient, _, err = fccrypto.EnsureCA(cfg)
		Expect(err).ToNot(HaveOccurred())

		// Create SSSD issuer with real components (no mocks for integration test)
		config := &config.SSSDOIDCIssuer{
			Issuer:       "https://test.example.com",
			Scopes:       []string{"openid", "profile", "email"},
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURIs: []string{"https://example.com/callback"},
		}

		provider, err = issuer.NewSSSDOIDCProvider(caClient, config)
		Expect(err).ToNot(HaveOccurred())
		Expect(provider).ToNot(BeNil())
	})

	AfterEach(func() {
		if provider != nil {
			provider.Close()
		}
	})

	Context("SSSD Issuer Integration", func() {
		It("should provide OpenID Configuration", func() {
			config := provider.GetOpenIDConfiguration("https://base.example.com")

			Expect(config).ToNot(BeNil())
			Expect(config["issuer"]).To(Equal("https://test.example.com"))
			Expect(config["scopes_supported"]).To(Equal([]string{"openid", "profile", "email"}))
			Expect(config["response_types_supported"]).To(ContainElement("code"))
			Expect(config["grant_types_supported"]).To(ContainElements("authorization_code", "refresh_token"))
		})

		It("should provide JWKS endpoint", func() {
			jwks, err := provider.GetJWKS()
			Expect(err).ToNot(HaveOccurred())
			Expect(jwks).ToNot(BeNil())
			Expect(jwks).To(HaveKey("keys"))
		})

		It("should handle authorization code flow with real SSSD", func() {
			// This test would require actual SSSD setup and real user authentication
			// For now, we'll test the interface compliance and basic functionality

			authParams := &v1alpha1.AuthAuthorizeParams{
				ClientId:     "test-client",
				RedirectUri:  "https://example.com/callback",
				ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
				State:        lo.ToPtr("test-state"),
			}

			// This will return a login form since no session is established
			authCode, err := provider.Authorize(ctx, authParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(authCode).To(ContainSubstring("<!DOCTYPE html>"))
			Expect(authCode).To(ContainSubstring("Flightctl Login"))
		})

		It("should handle token validation", func() {
			// Test with invalid token
			userInfo, err := provider.UserInfo(ctx, "invalid-token")
			Expect(err).To(HaveOccurred())
			Expect(userInfo.Error).ToNot(BeNil())
			Expect(*userInfo.Error).To(Equal("invalid_token"))
		})

		It("should handle unsupported grant types", func() {
			tokenReq := &v1alpha1.TokenRequest{
				GrantType: "unsupported_grant_type",
			}

			response, err := provider.Token(ctx, tokenReq)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.Error).ToNot(BeNil())
			Expect(*response.Error).To(Equal("unsupported_grant_type"))
		})

		It("should handle missing required fields in token request", func() {
			// Test missing code for authorization_code grant
			tokenReq := &v1alpha1.TokenRequest{
				GrantType: v1alpha1.AuthorizationCode,
				ClientId:  lo.ToPtr("test-client"),
			}

			response, err := provider.Token(ctx, tokenReq)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.Error).ToNot(BeNil())
			Expect(*response.Error).To(Equal("invalid_request"))
		})

		It("should handle invalid client credentials", func() {
			tokenReq := &v1alpha1.TokenRequest{
				GrantType: v1alpha1.AuthorizationCode,
				Code:      lo.ToPtr("valid-code"),
				ClientId:  lo.ToPtr("wrong-client"),
			}

			response, err := provider.Token(ctx, tokenReq)
			Expect(err).ToNot(HaveOccurred())
			Expect(response.Error).ToNot(BeNil())
			Expect(*response.Error).To(Equal("invalid_client"))
		})

		It("should implement OIDCIssuer interface", func() {
			// This test ensures the provider implements all required interface methods
			var _ issuer.OIDCIssuer = provider

			// Test all interface methods exist and can be called
			// Token method
			tokenReq := &v1alpha1.TokenRequest{
				GrantType: "unsupported",
			}
			tokenResp, err := provider.Token(ctx, tokenReq)
			Expect(err).ToNot(HaveOccurred())
			Expect(tokenResp).ToNot(BeNil())
			Expect(tokenResp.Error).ToNot(BeNil())

			// UserInfo method
			userInfoResp, err := provider.UserInfo(ctx, "invalid-token")
			Expect(err).To(HaveOccurred())
			Expect(userInfoResp.Error).ToNot(BeNil())

			// GetOpenIDConfiguration method
			oidcConfig := provider.GetOpenIDConfiguration("https://test.com")
			Expect(oidcConfig).ToNot(BeNil())
			Expect(oidcConfig).To(HaveKey("issuer"))

			// GetJWKS method
			jwks, err := provider.GetJWKS()
			Expect(err).ToNot(HaveOccurred())
			Expect(jwks).ToNot(BeNil())
		})
	})

	Context("SSSD Issuer with Real Authentication", func() {
		// These tests would require actual SSSD setup and real user accounts
		// They are marked as pending for now since they need a real SSSD environment

		PIt("should authenticate real users via SSSD", func() {
			// This test would require:
			// 1. Real SSSD configuration
			// 2. Real user accounts in the system
			// 3. Actual authentication flow

			// Example test structure:
			// authParams := &v1alpha1.AuthAuthorizeParams{...}
			// sessionID := "real-session-id"
			// ctx := context.WithValue(ctx, consts.SessionIDCtxKey, sessionID)
			//
			// authCode, err := provider.Authorize(ctx, authParams)
			// Expect(err).ToNot(HaveOccurred())
			//
			// tokenReq := &v1alpha1.TokenRequest{...}
			// response, err := provider.Token(ctx, tokenReq)
			// Expect(err).ToNot(HaveOccurred())
			// Expect(response.AccessToken).ToNot(BeNil())
		})

		PIt("should handle real user groups and roles", func() {
			// This test would verify that real system groups are properly mapped to roles
			// and that the user info endpoint returns correct information
		})

		PIt("should handle real refresh token flow", func() {
			// This test would verify the complete refresh token flow with real authentication
		})
	})
})
