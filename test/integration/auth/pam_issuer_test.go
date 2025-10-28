//go:build linux

package auth_test

import (
	"context"

	"github.com/flightctl/flightctl/api/v1alpha1"
	"github.com/flightctl/flightctl/internal/auth/issuer"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

var _ = Describe("PAM Issuer Integration Tests", func() {
	var (
		ctx      context.Context
		provider *issuer.PAMOIDCProvider
		caClient *fccrypto.CAClient
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Create test CA client
		cfg := ca.NewDefault(GinkgoT().TempDir())
		var err error
		caClient, _, err = fccrypto.EnsureCA(cfg)
		Expect(err).ToNot(HaveOccurred())

		// Create PAM issuer with real components (no mocks for integration test)
		config := &config.PAMOIDCIssuer{
			Issuer:       "https://test.example.com",
			Scopes:       []string{"openid", "profile", "email"},
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURIs: []string{"https://example.com/callback"},
			PAMService:   "other", // Use 'other' PAM service for authentication
		}

		provider, err = issuer.NewPAMOIDCProvider(caClient, config)
		Expect(err).ToNot(HaveOccurred())
		Expect(provider).ToNot(BeNil())
	})

	AfterEach(func() {
		if provider != nil {
			provider.Close()
		}
	})

	Context("PAM Issuer Integration", func() {
		It("should provide OpenID Configuration", func() {
			config, err := provider.GetOpenIDConfiguration("https://base.example.com")
			Expect(err).ToNot(HaveOccurred())

			Expect(config).ToNot(BeNil())
			Expect(config.Issuer).ToNot(BeNil())
			Expect(*config.Issuer).To(Equal("https://test.example.com"))
			Expect(config.ScopesSupported).ToNot(BeNil())
			Expect(*config.ScopesSupported).To(Equal([]string{"openid", "profile", "email"}))
			Expect(config.ResponseTypesSupported).ToNot(BeNil())
			Expect(*config.ResponseTypesSupported).To(ContainElement("code"))
			Expect(config.GrantTypesSupported).ToNot(BeNil())
			Expect(*config.GrantTypesSupported).To(ContainElements("authorization_code", "refresh_token"))
		})

		It("should provide JWKS endpoint", func() {
			jwks, err := provider.GetJWKS()
			Expect(err).ToNot(HaveOccurred())
			Expect(jwks).ToNot(BeNil())
			Expect(jwks.Keys).ToNot(BeNil())
		})

		It("should handle authorization code flow with real PAM", func() {
			// This test would require actual PAM setup and real user authentication
			// For now, we'll test the interface compliance and basic functionality

			authParams := &v1alpha1.AuthAuthorizeParams{
				ClientId:     "test-client",
				RedirectUri:  "https://example.com/callback",
				ResponseType: v1alpha1.AuthAuthorizeParamsResponseTypeCode,
				State:        lo.ToPtr("test-state"),
			}

			// This will return a login form since no session is established
			authResp, err := provider.Authorize(ctx, authParams)
			Expect(err).ToNot(HaveOccurred())
			Expect(authResp).ToNot(BeNil())
			Expect(authResp.Type).To(Equal(issuer.AuthorizeResponseTypeHTML))
			Expect(authResp.Content).To(ContainSubstring("<!DOCTYPE html>"))
			Expect(authResp.Content).To(ContainSubstring("Flightctl Login"))
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
			oidcConfig, err := provider.GetOpenIDConfiguration("https://test.com")
			Expect(err).ToNot(HaveOccurred())
			Expect(oidcConfig).ToNot(BeNil())
			Expect(oidcConfig.Issuer).ToNot(BeNil())

			// GetJWKS method
			jwks, err := provider.GetJWKS()
			Expect(err).ToNot(HaveOccurred())
			Expect(jwks).ToNot(BeNil())
		})
	})

})
