//go:build linux

package pam

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"os/user"
	"strings"
	"testing"
	"time"

	api "github.com/flightctl/flightctl/api/core/v1beta1"
	pamapi "github.com/flightctl/flightctl/api/pam-issuer/v1beta1"
	"github.com/flightctl/flightctl/internal/config"
	"github.com/flightctl/flightctl/internal/config/ca"
	fccrypto "github.com/flightctl/flightctl/internal/crypto"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/samber/lo"
	"golang.org/x/net/html"
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
	return []string{api.ExternalRoleAdmin}, nil
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
				Expect(response).ToNot(BeNil())
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

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidGrant))
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

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidGrant))
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
				Expect(response).ToNot(BeNil())
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

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidClient))
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

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidClient))
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
				Expect(response).ToNot(BeNil())
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

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidGrant))
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

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidClient))
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

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidGrant))
			})
		})
	})

	Context("Password Grant Flow Scenarios", func() {
		Context("Scenario 1: Public client with password grant", func() {
			BeforeEach(func() {
				config := &config.PAMOIDCIssuer{
					Issuer:       "https://test.example.com",
					Scopes:       []string{"openid", "profile", "email", "offline_access"},
					ClientID:     "public-client",
					ClientSecret: "", // No secret = public client
					RedirectURIs: []string{"https://example.com/callback"},
					PAMService:   "other",
				}

				var err error
				testProvider, err = NewPAMOIDCProviderWithAuthenticator(caClient, config, mockAuth)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should authenticate with valid credentials", func() {
				tokenReq := &pamapi.TokenRequest{
					GrantType: pamapi.Password,
					Username:  lo.ToPtr("testuser"),
					Password:  lo.ToPtr("testpassword"),
					ClientId:  lo.ToPtr("public-client"),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response).ToNot(BeNil())
				Expect(response.AccessToken).ToNot(BeEmpty())
				Expect(response.TokenType).To(Equal(pamapi.Bearer))
				Expect(response.ExpiresIn).ToNot(BeNil())
			})

			It("should return id_token when openid scope is requested", func() {
				tokenReq := &pamapi.TokenRequest{
					GrantType: pamapi.Password,
					Username:  lo.ToPtr("testuser"),
					Password:  lo.ToPtr("testpassword"),
					ClientId:  lo.ToPtr("public-client"),
					Scope:     lo.ToPtr("openid profile"),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response).ToNot(BeNil())
				Expect(response.AccessToken).ToNot(BeEmpty())
				Expect(response.IdToken).ToNot(BeNil())
				Expect(*response.IdToken).ToNot(BeEmpty())
			})

			It("should return refresh_token when offline_access scope is requested", func() {
				tokenReq := &pamapi.TokenRequest{
					GrantType: pamapi.Password,
					Username:  lo.ToPtr("testuser"),
					Password:  lo.ToPtr("testpassword"),
					ClientId:  lo.ToPtr("public-client"),
					Scope:     lo.ToPtr("openid offline_access"),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response).ToNot(BeNil())
				Expect(response.AccessToken).ToNot(BeEmpty())
				Expect(response.RefreshToken).ToNot(BeNil())
				Expect(*response.RefreshToken).ToNot(BeEmpty())
			})

			It("should not return refresh_token when offline_access scope is not requested", func() {
				tokenReq := &pamapi.TokenRequest{
					GrantType: pamapi.Password,
					Username:  lo.ToPtr("testuser"),
					Password:  lo.ToPtr("testpassword"),
					ClientId:  lo.ToPtr("public-client"),
					Scope:     lo.ToPtr("openid profile"),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response).ToNot(BeNil())
				Expect(response.RefreshToken).To(BeNil())
			})

			It("should reject request without username", func() {
				tokenReq := &pamapi.TokenRequest{
					GrantType: pamapi.Password,
					Password:  lo.ToPtr("testpassword"),
					ClientId:  lo.ToPtr("public-client"),
				}

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidRequest))
			})

			It("should reject request without password", func() {
				tokenReq := &pamapi.TokenRequest{
					GrantType: pamapi.Password,
					Username:  lo.ToPtr("testuser"),
					ClientId:  lo.ToPtr("public-client"),
				}

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidRequest))
			})

			It("should reject request with invalid credentials", func() {
				mockAuth.authenticateFunc = func(username, password string) error {
					return errors.New("authentication failed")
				}

				tokenReq := &pamapi.TokenRequest{
					GrantType: pamapi.Password,
					Username:  lo.ToPtr("testuser"),
					Password:  lo.ToPtr("wrongpassword"),
					ClientId:  lo.ToPtr("public-client"),
				}

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidGrant))
			})

			It("should reject request with wrong client_id", func() {
				tokenReq := &pamapi.TokenRequest{
					GrantType: pamapi.Password,
					Username:  lo.ToPtr("testuser"),
					Password:  lo.ToPtr("testpassword"),
					ClientId:  lo.ToPtr("wrong-client"),
				}

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidClient))
			})
		})

		Context("Scenario 2: Confidential client with password grant", func() {
			BeforeEach(func() {
				config := &config.PAMOIDCIssuer{
					Issuer:       "https://test.example.com",
					Scopes:       []string{"openid", "profile", "email", "offline_access"},
					ClientID:     "confidential-client",
					ClientSecret: "super-secret-value",
					RedirectURIs: []string{"https://example.com/callback"},
					PAMService:   "other",
				}

				var err error
				testProvider, err = NewPAMOIDCProviderWithAuthenticator(caClient, config, mockAuth)
				Expect(err).ToNot(HaveOccurred())
			})

			It("should authenticate with valid credentials and client secret", func() {
				tokenReq := &pamapi.TokenRequest{
					GrantType:    pamapi.Password,
					Username:     lo.ToPtr("testuser"),
					Password:     lo.ToPtr("testpassword"),
					ClientId:     lo.ToPtr("confidential-client"),
					ClientSecret: lo.ToPtr("super-secret-value"),
				}

				response, err := testProvider.Token(ctx, tokenReq)
				Expect(err).ToNot(HaveOccurred())
				Expect(response).ToNot(BeNil())
				Expect(response.AccessToken).ToNot(BeEmpty())
			})

			It("should reject request with wrong client secret", func() {
				tokenReq := &pamapi.TokenRequest{
					GrantType:    pamapi.Password,
					Username:     lo.ToPtr("testuser"),
					Password:     lo.ToPtr("testpassword"),
					ClientId:     lo.ToPtr("confidential-client"),
					ClientSecret: lo.ToPtr("wrong-secret"),
				}

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidClient))
			})

			It("should reject request without client secret", func() {
				tokenReq := &pamapi.TokenRequest{
					GrantType: pamapi.Password,
					Username:  lo.ToPtr("testuser"),
					Password:  lo.ToPtr("testpassword"),
					ClientId:  lo.ToPtr("confidential-client"),
				}

				_, err := testProvider.Token(ctx, tokenReq)
				Expect(err).To(HaveOccurred())
				oauth2Err, ok := pamapi.IsOAuth2Error(err)
				Expect(ok).To(BeTrue())
				Expect(oauth2Err.Code).To(Equal(pamapi.InvalidClient))
			})
		})
	})
})

// --- HTML parsing helpers for branding tests ---

// findNodeByAttr traverses the HTML tree and returns the first element with the given tag name and attribute key=val.
func findNodeByAttr(n *html.Node, tag, key, val string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		for _, a := range n.Attr {
			if a.Key == key && a.Val == val {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findNodeByAttr(c, tag, key, val); found != nil {
			return found
		}
	}
	return nil
}

// findNodeByID traverses the HTML tree and returns the first node with the given id attribute.
func findNodeByID(n *html.Node, id string) *html.Node {
	if n.Type == html.ElementNode {
		for _, a := range n.Attr {
			if a.Key == "id" && a.Val == id {
				return n
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findNodeByID(c, id); found != nil {
			return found
		}
	}
	return nil
}

// findNode traverses the HTML tree and returns the first node matching the tag name.
func findNode(n *html.Node, tag string) *html.Node {
	if n.Type == html.ElementNode && n.Data == tag {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if found := findNode(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// getAttr returns the value of the named attribute on a node.
func getAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// getTextContent returns the recursive text content of a node.
func getTextContent(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		sb.WriteString(getTextContent(c))
	}
	return sb.String()
}

// parseLoginForm parses the HTML returned by GetLoginForm and returns the document node.
func parseLoginForm(rawHTML string) *html.Node {
	doc, err := html.Parse(strings.NewReader(rawHTML))
	ExpectWithOffset(1, err).ToNot(HaveOccurred(), "failed to parse login form HTML")
	return doc
}

var _ = Describe("Login Form Branding", func() {
	var (
		mockAuth *MockAuthenticator
		caClient *fccrypto.CAClient
	)

	BeforeEach(func() {
		mockAuth = &MockAuthenticator{}

		cfg := ca.NewDefault(GinkgoT().TempDir())
		var err error
		caClient, _, err = fccrypto.EnsureCA(cfg)
		Expect(err).ToNot(HaveOccurred())
	})

	baseConfig := func() *config.PAMOIDCIssuer {
		return &config.PAMOIDCIssuer{
			Issuer:       "https://test.example.com",
			Scopes:       []string{"openid"},
			ClientID:     "test-client",
			ClientSecret: "test-secret",
			RedirectURIs: []string{"https://example.com/callback"},
			PAMService:   "test",
		}
	}

	Context("Default branding (no branding config)", func() {
		It("should use Flight Control defaults", func() {
			cfg := baseConfig()
			provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
			Expect(err).ToNot(HaveOccurred())
			defer provider.Close()

			rawHTML := provider.GetLoginForm()
			doc := parseLoginForm(rawHTML)

			titleNode := findNode(doc, "title")
			Expect(titleNode).ToNot(BeNil(), "expected <title> element")
			Expect(getTextContent(titleNode)).To(Equal("Flight Control Login"))

			faviconLink := findNodeByAttr(doc, "link", "rel", "icon")
			Expect(faviconLink).ToNot(BeNil(), "expected <link rel=\"icon\"> element")
			Expect(getAttr(faviconLink, "href")).To(Equal("/auth/assets/favicon.png"))

			lightLogo := findNodeByID(doc, "brand-logo-light")
			Expect(lightLogo).ToNot(BeNil(), "expected #brand-logo-light element")
			Expect(getAttr(lightLogo, "src")).To(Equal("/auth/assets/flight-control-logo.svg"))
			Expect(getAttr(lightLogo, "alt")).To(Equal("Flight Control"))

			darkLogo := findNodeByID(doc, "brand-logo-dark")
			Expect(darkLogo).ToNot(BeNil(), "expected #brand-logo-dark element")
			Expect(getAttr(darkLogo, "src")).To(Equal("/auth/assets/flight-control-logo.svg"))

			Expect(rawHTML).NotTo(ContainSubstring("--pf-t--global--color--brand--default"))
		})
	})

	Context("Custom display name", func() {
		It("should use the configured display name in title and heading", func() {
			cfg := baseConfig()
			cfg.Branding = &config.LoginBranding{
				DisplayName: "ACME Corp",
			}
			provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
			Expect(err).ToNot(HaveOccurred())
			defer provider.Close()

			rawHTML := provider.GetLoginForm()
			doc := parseLoginForm(rawHTML)

			titleNode := findNode(doc, "title")
			Expect(titleNode).ToNot(BeNil())
			Expect(getTextContent(titleNode)).To(Equal("ACME Corp Login"))

			logo := findNodeByID(doc, "brand-logo-light")
			Expect(logo).ToNot(BeNil())
			Expect(getAttr(logo, "alt")).To(Equal("ACME Corp"))
		})
	})

	Context("Custom logo data URI per theme", func() {
		It("should use per-theme logo data URIs", func() {
			cfg := baseConfig()
			lightURI := "data:image/svg+xml;base64,PHN2Zy8+light"
			darkURI := "data:image/svg+xml;base64,PHN2Zy8+dark"
			cfg.Branding = &config.LoginBranding{
				LightTheme: &config.ThemeColors{
					LogoDataUri: lightURI,
				},
				DarkTheme: &config.ThemeColors{
					LogoDataUri: darkURI,
				},
			}
			provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
			Expect(err).ToNot(HaveOccurred())
			defer provider.Close()

			rawHTML := provider.GetLoginForm()
			doc := parseLoginForm(rawHTML)

			lightLogo := findNodeByID(doc, "brand-logo-light")
			Expect(lightLogo).ToNot(BeNil(), "expected #brand-logo-light element")
			Expect(getAttr(lightLogo, "src")).To(Equal(lightURI))

			darkLogo := findNodeByID(doc, "brand-logo-dark")
			Expect(darkLogo).ToNot(BeNil(), "expected #brand-logo-dark element")
			Expect(getAttr(darkLogo, "src")).To(Equal(darkURI))
		})

		It("should fall back to default logo when only one theme has a logo", func() {
			cfg := baseConfig()
			darkURI := "data:image/svg+xml;base64,PHN2Zy8+dark"
			cfg.Branding = &config.LoginBranding{
				DarkTheme: &config.ThemeColors{
					LogoDataUri: darkURI,
				},
			}
			provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
			Expect(err).ToNot(HaveOccurred())
			defer provider.Close()

			rawHTML := provider.GetLoginForm()
			doc := parseLoginForm(rawHTML)

			lightLogo := findNodeByID(doc, "brand-logo-light")
			Expect(lightLogo).ToNot(BeNil())
			Expect(getAttr(lightLogo, "src")).To(Equal("/auth/assets/flight-control-logo.svg"))

			darkLogo := findNodeByID(doc, "brand-logo-dark")
			Expect(darkLogo).ToNot(BeNil())
			Expect(getAttr(darkLogo, "src")).To(Equal(darkURI))
		})
	})

	Context("Custom favicon data URI", func() {
		It("should use the configured favicon data URI", func() {
			cfg := baseConfig()
			faviconURI := "data:image/png;base64,iVBORw0KGgoAAAANS"
			cfg.Branding = &config.LoginBranding{
				FaviconDataUri: faviconURI,
			}
			provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
			Expect(err).ToNot(HaveOccurred())
			defer provider.Close()

			rawHTML := provider.GetLoginForm()
			doc := parseLoginForm(rawHTML)

			faviconLink := findNodeByAttr(doc, "link", "rel", "icon")
			Expect(faviconLink).ToNot(BeNil(), "expected <link rel=\"icon\"> element")
			Expect(getAttr(faviconLink, "href")).To(Equal(faviconURI))
		})
	})

	Context("Light theme color overrides", func() {
		It("should inject CSS variable overrides in :root scope", func() {
			cfg := baseConfig()
			cfg.Branding = &config.LoginBranding{
				LightTheme: &config.ThemeColors{
					BrandDefault:        "#0066cc",
					BrandHover:          "#004499",
					BrandClicked:        "#004499",
					BackgroundSecondary: "#f0f0f0",
					BackgroundPrimary:   "#ffffff",
					TextColorRegular:    "#333333",
				},
			}
			provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
			Expect(err).ToNot(HaveOccurred())
			defer provider.Close()

			css := provider.GetLoginCSS()
			Expect(css).To(ContainSubstring("--pf-t--global--color--brand--default: #0066cc"))
			Expect(css).To(ContainSubstring("--pf-t--global--color--brand--hover: #004499"))
			Expect(css).To(ContainSubstring("--pf-t--global--color--brand--clicked: #004499"))
			Expect(css).To(ContainSubstring("--pf-t--global--background--color--secondary--default: #f0f0f0"))
			Expect(css).To(ContainSubstring("--pf-t--global--background--color--primary--default: #ffffff"))
			Expect(css).To(ContainSubstring("--pf-t--global--text--color--regular: #333333"))
		})
	})

	Context("Dark theme color overrides", func() {
		It("should inject CSS variable overrides in .pf-v6-theme-dark scope", func() {
			cfg := baseConfig()
			cfg.Branding = &config.LoginBranding{
				DarkTheme: &config.ThemeColors{
					BrandDefault:        "#4da6ff",
					BrandHover:          "#3d96ef",
					BackgroundSecondary: "#1a1a2e",
				},
			}
			provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
			Expect(err).ToNot(HaveOccurred())
			defer provider.Close()

			css := provider.GetLoginCSS()
			Expect(css).To(ContainSubstring(".pf-v6-theme-dark"))
			Expect(css).To(ContainSubstring("--pf-t--global--color--brand--default: #4da6ff"))
			Expect(css).To(ContainSubstring("--pf-t--global--color--brand--hover: #3d96ef"))
			Expect(css).NotTo(ContainSubstring("--pf-t--global--color--brand--clicked"))
			Expect(css).To(ContainSubstring("--pf-t--global--background--color--secondary--default: #1a1a2e"))
		})
	})

	Context("Partial config (display name only, no themes)", func() {
		It("should not emit CSS override block", func() {
			cfg := baseConfig()
			cfg.Branding = &config.LoginBranding{
				DisplayName: "My Company",
			}
			provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
			Expect(err).ToNot(HaveOccurred())
			defer provider.Close()

			rawHTML := provider.GetLoginForm()
			doc := parseLoginForm(rawHTML)

			titleNode := findNode(doc, "title")
			Expect(titleNode).ToNot(BeNil())
			Expect(getTextContent(titleNode)).To(Equal("My Company Login"))

			css := provider.GetLoginCSS()
			Expect(css).NotTo(ContainSubstring("--pf-t--global--color--brand--default"))
		})
	})

	Context("Both light and dark themes", func() {
		It("should emit overrides for both scopes", func() {
			cfg := baseConfig()
			cfg.Branding = &config.LoginBranding{
				DisplayName: "Branded App",
				LightTheme: &config.ThemeColors{
					BrandDefault: "#0066cc",
				},
				DarkTheme: &config.ThemeColors{
					BrandDefault: "#4da6ff",
				},
			}
			provider, err := NewPAMOIDCProviderWithAuthenticator(caClient, cfg, mockAuth)
			Expect(err).ToNot(HaveOccurred())
			defer provider.Close()

			rawHTML := provider.GetLoginForm()
			doc := parseLoginForm(rawHTML)

			titleNode := findNode(doc, "title")
			Expect(titleNode).ToNot(BeNil())
			Expect(getTextContent(titleNode)).To(Equal("Branded App Login"))

			css := provider.GetLoginCSS()
			Expect(css).To(ContainSubstring("--pf-t--global--color--brand--default: #0066cc"))
			Expect(css).To(ContainSubstring(".pf-v6-theme-dark"))
			Expect(css).To(ContainSubstring("--pf-t--global--color--brand--default: #4da6ff"))
		})
	})
})
