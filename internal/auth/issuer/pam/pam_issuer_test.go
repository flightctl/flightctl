//go:build linux

package pam

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"os/user"
	"testing"
	"time"

	pamapi "github.com/flightctl/flightctl/api/v1alpha1/pam-issuer"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
)

// MockAuthenticator is a mock implementation of the Authenticator interface for testing
type MockAuthenticator struct {
	authenticateFunc  func(username, password string) error
	lookupUserFunc    func(username string) (*user.User, error)
	getUserGroupsFunc func(systemUser *user.User) ([]string, error)
}

func (m *MockAuthenticator) Authenticate(username, password string) error {
	if m.authenticateFunc != nil {
		return m.authenticateFunc(username, password)
	}
	return nil
}

func (m *MockAuthenticator) LookupUser(username string) (*user.User, error) {
	if m.lookupUserFunc != nil {
		return m.lookupUserFunc(username)
	}
	return &user.User{
		Username: username,
		Uid:      "1000",
		Gid:      "1000",
		Name:     "Test User",
		HomeDir:  "/home/" + username,
	}, nil
}

func (m *MockAuthenticator) GetUserGroups(systemUser *user.User) ([]string, error) {
	if m.getUserGroupsFunc != nil {
		return m.getUserGroupsFunc(systemUser)
	}
	return []string{"flightctl-admin"}, nil
}

func (m *MockAuthenticator) Close() error {
	return nil
}

// Helper function to generate PKCE challenge from verifier
func generatePKCEChallenge(verifier string) string {
	hash := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}

func TestPAMIssuer(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "PAM Issuer Unit Test Suite")
}

var _ = Describe("PAM Issuer Unit Tests", func() {
	var (
		ctx          context.Context
		mockAuth     *MockAuthenticator
		testProvider *PAMOIDCProvider
		caClient     *fccrypto.CAClient
	)

	BeforeEach(func() {
		ctx = context.Background()
		mockAuth = &MockAuthenticator{}

		// Create test CA client
		cfg := ca.NewDefault(GinkgoT().TempDir())
		var err error
		caClient, _, err = fccrypto.EnsureCA(cfg)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		if testProvider != nil {
			testProvider.Close()
		}
	})

	Context("PKCE Authorization Flow Scenarios", func() {
		Context("Scenario 1: Public client with PKCE (required)", func() {
			BeforeEach(func() {
				config := &config.PAMOIDCIssuer{
					Issuer:       "https://test.example.com",
					Scopes:       []string{"openid", "profile", "email"},
					ClientID:     "public-client",
					ClientSecret: "", // No secret = public client
					RedirectURIs: []string{"https://example.com/callback"},
					PAMService:   "other",
				}

				var err error
				testProvider, err = NewPAMOIDCProviderWithAuthenticator(caClient, config, mockAuth)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should accept authorization request with valid PKCE", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authParams := &pamapi.AuthAuthorizeParams{
					ClientId:            "public-client",
					RedirectUri:         "https://example.com/callback",
					ResponseType:        pamapi.Code,
					State:               lo.ToPtr("test-state"),
					CodeChallenge:       lo.ToPtr(codeChallenge),
					CodeChallengeMethod: lo.ToPtr(pamapi.AuthAuthorizeParamsCodeChallengeMethodS256),
				}

				authResp, err := testProvider.Authorize(ctx, authParams)
				Expect(err).ToNot(HaveOccurred())
				Expect(authResp).ToNot(BeNil())
				Expect(authResp.Type).To(Equal(AuthorizeResponseTypeHTML))
			})

			It("should reject authorization request without PKCE", func() {
				authParams := &pamapi.AuthAuthorizeParams{
					ClientId:     "public-client",
					RedirectUri:  "https://example.com/callback",
					ResponseType: pamapi.Code,
					State:        lo.ToPtr("test-state"),
				}

				_, err := testProvider.Authorize(ctx, authParams)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid_request"))
			})

			It("should reject authorization request with code_challenge but no method", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authParams := &pamapi.AuthAuthorizeParams{
					ClientId:      "public-client",
					RedirectUri:   "https://example.com/callback",
					ResponseType:  pamapi.Code,
					State:         lo.ToPtr("test-state"),
					CodeChallenge: lo.ToPtr(codeChallenge),
				}

				_, err := testProvider.Authorize(ctx, authParams)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("invalid_request"))
			})

			It("should verify PKCE correctly at token endpoint", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authCode := "test-auth-code"
				codeData := &AuthorizationCodeData{
					Code:                authCode,
					ClientID:            "public-client",
					RedirectURI:         "https://example.com/callback",
					Scope:               "openid profile",
					State:               "test-state",
					Username:            "testuser",
					ExpiresAt:           time.Now().Add(10 * time.Minute),
					CreatedAt:           time.Now(),
					CodeChallenge:       codeChallenge,
					CodeChallengeMethod: pamapi.AuthAuthorizeParamsCodeChallengeMethodS256,
				}
				testProvider.codeStore.StoreCode(codeData)

				tokenReq := &pamapi.TokenRequest{
					GrantType:    pamapi.AuthorizationCode,
					Code:         lo.ToPtr(authCode),
					ClientId:     lo.ToPtr("public-client"),
					RedirectUri:  lo.ToPtr("https://example.com/callback"),
					CodeVerifier: lo.ToPtr(codeVerifier),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.Error).To(BeNil())
				Expect(response.AccessToken).ToNot(BeNil())
			})

			It("should reject token request with wrong PKCE verifier", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authCode := "test-auth-code"
				codeData := &AuthorizationCodeData{
					Code:                authCode,
					ClientID:            "public-client",
					RedirectURI:         "https://example.com/callback",
					Scope:               "openid profile",
					State:               "test-state",
					Username:            "testuser",
					ExpiresAt:           time.Now().Add(10 * time.Minute),
					CreatedAt:           time.Now(),
					CodeChallenge:       codeChallenge,
					CodeChallengeMethod: pamapi.AuthAuthorizeParamsCodeChallengeMethodS256,
				}
				testProvider.codeStore.StoreCode(codeData)

				tokenReq := &pamapi.TokenRequest{
					GrantType:    pamapi.AuthorizationCode,
					Code:         lo.ToPtr(authCode),
					ClientId:     lo.ToPtr("public-client"),
					RedirectUri:  lo.ToPtr("https://example.com/callback"),
					CodeVerifier: lo.ToPtr("wrong-verifier"),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.Error).ToNot(BeNil())
				Expect(*response.Error).To(Equal("invalid_grant"))
			})

			It("should reject token request with missing PKCE verifier", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authCode := "test-auth-code"
				codeData := &AuthorizationCodeData{
					Code:                authCode,
					ClientID:            "public-client",
					RedirectURI:         "https://example.com/callback",
					Scope:               "openid profile",
					State:               "test-state",
					Username:            "testuser",
					ExpiresAt:           time.Now().Add(10 * time.Minute),
					CreatedAt:           time.Now(),
					CodeChallenge:       codeChallenge,
					CodeChallengeMethod: pamapi.AuthAuthorizeParamsCodeChallengeMethodS256,
				}
				testProvider.codeStore.StoreCode(codeData)

				tokenReq := &pamapi.TokenRequest{
					GrantType:   pamapi.AuthorizationCode,
					Code:        lo.ToPtr(authCode),
					ClientId:    lo.ToPtr("public-client"),
					RedirectUri: lo.ToPtr("https://example.com/callback"),
					// No CodeVerifier provided
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.Error).ToNot(BeNil())
				Expect(*response.Error).To(Equal("invalid_grant"))
			})
		})

		Context("Scenario 2: Public client without PKCE (explicitly allowed by config)", func() {
			BeforeEach(func() {
				config := &config.PAMOIDCIssuer{
					Issuer:                       "https://test.example.com",
					Scopes:                       []string{"openid", "profile", "email"},
					ClientID:                     "public-client-no-pkce",
					ClientSecret:                 "", // No secret = public client
					RedirectURIs:                 []string{"https://example.com/callback"},
					PAMService:                   "other",
					AllowPublicClientWithoutPKCE: true,
				}

				var err error
				testProvider, err = NewPAMOIDCProviderWithAuthenticator(caClient, config, mockAuth)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should accept authorization request without PKCE when allowed", func() {
				authParams := &pamapi.AuthAuthorizeParams{
					ClientId:     "public-client-no-pkce",
					RedirectUri:  "https://example.com/callback",
					ResponseType: pamapi.Code,
					State:        lo.ToPtr("test-state"),
				}

				authResp, err := testProvider.Authorize(ctx, authParams)
				Expect(err).ToNot(HaveOccurred())
				Expect(authResp).ToNot(BeNil())
				Expect(authResp.Type).To(Equal(AuthorizeResponseTypeHTML))
			})

			It("should still accept authorization request with PKCE", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authParams := &pamapi.AuthAuthorizeParams{
					ClientId:            "public-client-no-pkce",
					RedirectUri:         "https://example.com/callback",
					ResponseType:        pamapi.Code,
					State:               lo.ToPtr("test-state"),
					CodeChallenge:       lo.ToPtr(codeChallenge),
					CodeChallengeMethod: lo.ToPtr(pamapi.AuthAuthorizeParamsCodeChallengeMethodS256),
				}

				authResp, err := testProvider.Authorize(ctx, authParams)
				Expect(err).ToNot(HaveOccurred())
				Expect(authResp).ToNot(BeNil())
				Expect(authResp.Type).To(Equal(AuthorizeResponseTypeHTML))
			})
		})

		Context("Scenario 3: Confidential client without PKCE (secret-based auth)", func() {
			BeforeEach(func() {
				config := &config.PAMOIDCIssuer{
					Issuer:       "https://test.example.com",
					Scopes:       []string{"openid", "profile", "email"},
					ClientID:     "confidential-client",
					ClientSecret: "super-secret-value",
					RedirectURIs: []string{"https://example.com/callback"},
					PAMService:   "other",
				}

				var err error
				testProvider, err = NewPAMOIDCProviderWithAuthenticator(caClient, config, mockAuth)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should accept authorization request without PKCE", func() {
				authParams := &pamapi.AuthAuthorizeParams{
					ClientId:     "confidential-client",
					RedirectUri:  "https://example.com/callback",
					ResponseType: pamapi.Code,
					State:        lo.ToPtr("test-state"),
				}

				authResp, err := testProvider.Authorize(ctx, authParams)
				Expect(err).ToNot(HaveOccurred())
				Expect(authResp).ToNot(BeNil())
				Expect(authResp.Type).To(Equal(AuthorizeResponseTypeHTML))
			})

			It("should require client secret at token endpoint", func() {
				authCode := "test-auth-code"
				codeData := &AuthorizationCodeData{
					Code:        authCode,
					ClientID:    "confidential-client",
					RedirectURI: "https://example.com/callback",
					Scope:       "openid profile",
					State:       "test-state",
					Username:    "testuser",
					ExpiresAt:   time.Now().Add(10 * time.Minute),
					CreatedAt:   time.Now(),
				}
				testProvider.codeStore.StoreCode(codeData)

				tokenReq := &pamapi.TokenRequest{
					GrantType:    pamapi.AuthorizationCode,
					Code:         lo.ToPtr(authCode),
					ClientId:     lo.ToPtr("confidential-client"),
					RedirectUri:  lo.ToPtr("https://example.com/callback"),
					ClientSecret: lo.ToPtr("super-secret-value"),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.Error).To(BeNil())
				Expect(response.AccessToken).ToNot(BeNil())
			})

			It("should reject token request with wrong client secret", func() {
				authCode := "test-auth-code"
				codeData := &AuthorizationCodeData{
					Code:        authCode,
					ClientID:    "confidential-client",
					RedirectURI: "https://example.com/callback",
					Scope:       "openid profile",
					State:       "test-state",
					Username:    "testuser",
					ExpiresAt:   time.Now().Add(10 * time.Minute),
					CreatedAt:   time.Now(),
				}
				testProvider.codeStore.StoreCode(codeData)

				tokenReq := &pamapi.TokenRequest{
					GrantType:    pamapi.AuthorizationCode,
					Code:         lo.ToPtr(authCode),
					ClientId:     lo.ToPtr("confidential-client"),
					RedirectUri:  lo.ToPtr("https://example.com/callback"),
					ClientSecret: lo.ToPtr("wrong-secret"),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.Error).ToNot(BeNil())
				Expect(*response.Error).To(Equal("invalid_client"))
			})

			It("should reject token request without client secret", func() {
				authCode := "test-auth-code"
				codeData := &AuthorizationCodeData{
					Code:        authCode,
					ClientID:    "confidential-client",
					RedirectURI: "https://example.com/callback",
					Scope:       "openid profile",
					State:       "test-state",
					Username:    "testuser",
					ExpiresAt:   time.Now().Add(10 * time.Minute),
					CreatedAt:   time.Now(),
				}
				testProvider.codeStore.StoreCode(codeData)

				tokenReq := &pamapi.TokenRequest{
					GrantType:   pamapi.AuthorizationCode,
					Code:        lo.ToPtr(authCode),
					ClientId:    lo.ToPtr("confidential-client"),
					RedirectUri: lo.ToPtr("https://example.com/callback"),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.Error).ToNot(BeNil())
				Expect(*response.Error).To(Equal("invalid_client"))
			})
		})

		Context("Scenario 4: Confidential client with PKCE (extra security)", func() {
			BeforeEach(func() {
				config := &config.PAMOIDCIssuer{
					Issuer:       "https://test.example.com",
					Scopes:       []string{"openid", "profile", "email"},
					ClientID:     "confidential-client-pkce",
					ClientSecret: "super-secret-value",
					RedirectURIs: []string{"https://example.com/callback"},
					PAMService:   "other",
				}

				var err error
				testProvider, err = NewPAMOIDCProviderWithAuthenticator(caClient, config, mockAuth)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should accept authorization request with PKCE", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authParams := &pamapi.AuthAuthorizeParams{
					ClientId:            "confidential-client-pkce",
					RedirectUri:         "https://example.com/callback",
					ResponseType:        pamapi.Code,
					State:               lo.ToPtr("test-state"),
					CodeChallenge:       lo.ToPtr(codeChallenge),
					CodeChallengeMethod: lo.ToPtr(pamapi.AuthAuthorizeParamsCodeChallengeMethodS256),
				}

				authResp, err := testProvider.Authorize(ctx, authParams)
				Expect(err).ToNot(HaveOccurred())
				Expect(authResp).ToNot(BeNil())
				Expect(authResp.Type).To(Equal(AuthorizeResponseTypeHTML))
			})

			It("should require both client secret and PKCE verifier at token endpoint", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authCode := "test-auth-code"
				codeData := &AuthorizationCodeData{
					Code:                authCode,
					ClientID:            "confidential-client-pkce",
					RedirectURI:         "https://example.com/callback",
					Scope:               "openid profile",
					State:               "test-state",
					Username:            "testuser",
					ExpiresAt:           time.Now().Add(10 * time.Minute),
					CreatedAt:           time.Now(),
					CodeChallenge:       codeChallenge,
					CodeChallengeMethod: pamapi.AuthAuthorizeParamsCodeChallengeMethodS256,
				}
				testProvider.codeStore.StoreCode(codeData)

				tokenReq := &pamapi.TokenRequest{
					GrantType:    pamapi.AuthorizationCode,
					Code:         lo.ToPtr(authCode),
					ClientId:     lo.ToPtr("confidential-client-pkce"),
					RedirectUri:  lo.ToPtr("https://example.com/callback"),
					ClientSecret: lo.ToPtr("super-secret-value"),
					CodeVerifier: lo.ToPtr(codeVerifier),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.Error).To(BeNil())
				Expect(response.AccessToken).ToNot(BeNil())
			})

			It("should reject token request with correct secret but wrong PKCE verifier", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authCode := "test-auth-code"
				codeData := &AuthorizationCodeData{
					Code:                authCode,
					ClientID:            "confidential-client-pkce",
					RedirectURI:         "https://example.com/callback",
					Scope:               "openid profile",
					State:               "test-state",
					Username:            "testuser",
					ExpiresAt:           time.Now().Add(10 * time.Minute),
					CreatedAt:           time.Now(),
					CodeChallenge:       codeChallenge,
					CodeChallengeMethod: pamapi.AuthAuthorizeParamsCodeChallengeMethodS256,
				}
				testProvider.codeStore.StoreCode(codeData)

				tokenReq := &pamapi.TokenRequest{
					GrantType:    pamapi.AuthorizationCode,
					Code:         lo.ToPtr(authCode),
					ClientId:     lo.ToPtr("confidential-client-pkce"),
					RedirectUri:  lo.ToPtr("https://example.com/callback"),
					ClientSecret: lo.ToPtr("super-secret-value"),
					CodeVerifier: lo.ToPtr("wrong-verifier"),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.Error).ToNot(BeNil())
				Expect(*response.Error).To(Equal("invalid_grant"))
			})

			It("should reject token request with correct PKCE but wrong secret", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authCode := "test-auth-code"
				codeData := &AuthorizationCodeData{
					Code:                authCode,
					ClientID:            "confidential-client-pkce",
					RedirectURI:         "https://example.com/callback",
					Scope:               "openid profile",
					State:               "test-state",
					Username:            "testuser",
					ExpiresAt:           time.Now().Add(10 * time.Minute),
					CreatedAt:           time.Now(),
					CodeChallenge:       codeChallenge,
					CodeChallengeMethod: pamapi.AuthAuthorizeParamsCodeChallengeMethodS256,
				}
				testProvider.codeStore.StoreCode(codeData)

				tokenReq := &pamapi.TokenRequest{
					GrantType:    pamapi.AuthorizationCode,
					Code:         lo.ToPtr(authCode),
					ClientId:     lo.ToPtr("confidential-client-pkce"),
					RedirectUri:  lo.ToPtr("https://example.com/callback"),
					ClientSecret: lo.ToPtr("wrong-secret"),
					CodeVerifier: lo.ToPtr(codeVerifier),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.Error).ToNot(BeNil())
				Expect(*response.Error).To(Equal("invalid_client"))
			})

			It("should reject token request with missing PKCE verifier when challenge was provided", func() {
				codeVerifier := "test-verifier-with-sufficient-entropy-for-pkce"
				codeChallenge := generatePKCEChallenge(codeVerifier)

				authCode := "test-auth-code"
				codeData := &AuthorizationCodeData{
					Code:                authCode,
					ClientID:            "confidential-client-pkce",
					RedirectURI:         "https://example.com/callback",
					Scope:               "openid profile",
					State:               "test-state",
					Username:            "testuser",
					ExpiresAt:           time.Now().Add(10 * time.Minute),
					CreatedAt:           time.Now(),
					CodeChallenge:       codeChallenge,
					CodeChallengeMethod: pamapi.AuthAuthorizeParamsCodeChallengeMethodS256,
				}
				testProvider.codeStore.StoreCode(codeData)

				tokenReq := &pamapi.TokenRequest{
					GrantType:    pamapi.AuthorizationCode,
					Code:         lo.ToPtr(authCode),
					ClientId:     lo.ToPtr("confidential-client-pkce"),
					RedirectUri:  lo.ToPtr("https://example.com/callback"),
					ClientSecret: lo.ToPtr("super-secret-value"),
					// Missing CodeVerifier
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response.Error).ToNot(BeNil())
				Expect(*response.Error).To(Equal("invalid_grant"))
			})
		})
	})
})
